package llmproxy

import (
	"bytes"
	"encoding/json"
	"strings"
)

// TokenUsage is the provider-reported token split for one completed request.
// The has* flags distinguish "not reported" from a real zero.
type TokenUsage struct {
	Model      string
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64

	hasModel      bool
	hasInput      bool
	hasOutput     bool
	hasCacheRead  bool
	hasCacheWrite bool
}

func (u TokenUsage) found() bool {
	return u.hasInput || u.hasOutput || u.hasCacheRead || u.hasCacheWrite
}

type tokenDetails struct {
	CachedTokens *int64 `json:"cached_tokens"`
}

type usageWire struct {
	// pi-native (AssistantMessage.usage).
	Input      *int64 `json:"input"`
	Output     *int64 `json:"output"`
	CacheRead  *int64 `json:"cacheRead"`
	CacheWrite *int64 `json:"cacheWrite"`
	// OpenAI Chat Completions.
	PromptTokens     *int64        `json:"prompt_tokens"`
	CompletionTokens *int64        `json:"completion_tokens"`
	PromptDetails    *tokenDetails `json:"prompt_tokens_details"`
	// Anthropic Messages and OpenAI Responses.
	InputTokens        *int64        `json:"input_tokens"`
	OutputTokens       *int64        `json:"output_tokens"`
	CacheReadInput     *int64        `json:"cache_read_input_tokens"`
	CacheCreationInput *int64        `json:"cache_creation_input_tokens"`
	InputTokensDetails *tokenDetails `json:"input_tokens_details"`
}

type usageEnvelope struct {
	Model    string      `json:"model"`
	Usage    *usageWire  `json:"usage"`
	Message  *usageInner `json:"message"`
	Response *usageInner `json:"response"`
}

type usageInner struct {
	Model string     `json:"model"`
	Usage *usageWire `json:"usage"`
}

// toUsage normalizes the three wire families the gateway serves.
func (w *usageWire) toUsage() TokenUsage {
	var usage TokenUsage
	switch {
	case w.Input != nil || w.Output != nil:
		usage.hasInput = w.Input != nil
		usage.hasOutput = w.Output != nil
		if w.Input != nil {
			usage.Input = *w.Input
		}
		if w.Output != nil {
			usage.Output = *w.Output
		}
		usage.hasCacheRead = w.CacheRead != nil
		if w.CacheRead != nil {
			usage.CacheRead = *w.CacheRead
		}
		usage.hasCacheWrite = w.CacheWrite != nil
		if w.CacheWrite != nil {
			usage.CacheWrite = *w.CacheWrite
		}
	case w.PromptTokens != nil || w.CompletionTokens != nil:
		usage.hasInput = w.PromptTokens != nil
		usage.hasOutput = w.CompletionTokens != nil
		if w.PromptTokens != nil {
			usage.Input = *w.PromptTokens
		}
		if w.CompletionTokens != nil {
			usage.Output = *w.CompletionTokens
		}
		if w.PromptDetails != nil && w.PromptDetails.CachedTokens != nil {
			usage.hasCacheRead = true
			usage.CacheRead = *w.PromptDetails.CachedTokens
		}
	case w.InputTokens != nil || w.OutputTokens != nil:
		usage.hasInput = w.InputTokens != nil
		usage.hasOutput = w.OutputTokens != nil
		if w.InputTokens != nil {
			usage.Input = *w.InputTokens
		}
		if w.OutputTokens != nil {
			usage.Output = *w.OutputTokens
		}
		if w.CacheReadInput != nil {
			usage.hasCacheRead = true
			usage.CacheRead = *w.CacheReadInput
		}
		if w.CacheCreationInput != nil {
			usage.hasCacheWrite = true
			usage.CacheWrite = *w.CacheCreationInput
		}
		if w.InputTokensDetails != nil && w.InputTokensDetails.CachedTokens != nil {
			usage.hasCacheRead = true
			usage.CacheRead = *w.InputTokensDetails.CachedTokens
		}
	}
	return usage
}

func parseEnvelope(raw []byte) (TokenUsage, bool) {
	var envelope usageEnvelope
	if json.Unmarshal(raw, &envelope) != nil {
		return TokenUsage{}, false
	}
	var out TokenUsage
	if envelope.Message != nil {
		if envelope.Message.Usage != nil {
			out = envelope.Message.Usage.toUsage()
		}
		if envelope.Message.Model != "" {
			out.Model, out.hasModel = envelope.Message.Model, true
		}
	} else if envelope.Response != nil {
		if envelope.Response.Usage != nil {
			out = envelope.Response.Usage.toUsage()
		}
		if envelope.Response.Model != "" {
			out.Model, out.hasModel = envelope.Response.Model, true
		}
	} else if envelope.Usage != nil {
		out = envelope.Usage.toUsage()
	}
	if envelope.Model != "" {
		out.Model, out.hasModel = envelope.Model, true
	}
	return out, out.found()
}

func mergeUsage(dst, src TokenUsage) TokenUsage {
	if src.hasModel {
		dst.Model, dst.hasModel = src.Model, true
	}
	if src.hasInput {
		dst.Input, dst.hasInput = src.Input, true
	}
	if src.hasOutput {
		dst.Output, dst.hasOutput = src.Output, true
	}
	if src.hasCacheRead {
		dst.CacheRead, dst.hasCacheRead = src.CacheRead, true
	}
	if src.hasCacheWrite {
		dst.CacheWrite, dst.hasCacheWrite = src.CacheWrite, true
	}
	return dst
}

// ExtractUsage reads usage from a non-streaming JSON body or an SSE stream.
// SSE is scanned line by line and merged, because Anthropic reports input
// tokens in message_start and output tokens in message_delta.
func ExtractUsage(body []byte) (TokenUsage, bool) {
	var merged TokenUsage
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		if usage, ok := parseEnvelope(trimmed); ok {
			merged = mergeUsage(merged, usage)
		}
	}
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) || payload[0] != '{' {
			continue
		}
		if usage, ok := parseEnvelope(payload); ok {
			merged = mergeUsage(merged, usage)
		}
	}
	return merged, merged.found()
}

// requestModel extracts the model selector from the inbound request body. The
// gateway accepts OpenAI/Anthropic `model` and pi-native `modelId`.
func requestModel(body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	var envelope struct {
		Model   json.RawMessage `json:"model"`
		ModelID string          `json:"modelId"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	if strings.TrimSpace(envelope.ModelID) != "" {
		return strings.TrimSpace(envelope.ModelID)
	}
	if len(envelope.Model) == 0 {
		return ""
	}
	var model string
	if json.Unmarshal(envelope.Model, &model) == nil {
		return strings.TrimSpace(model)
	}
	var object struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(envelope.Model, &object) == nil {
		return strings.TrimSpace(object.ID)
	}
	return ""
}
