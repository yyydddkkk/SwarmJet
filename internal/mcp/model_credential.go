package mcp

import (
	"context"
	"fmt"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yyydddkkk/SwarmJet/internal/llmproxy"
)

// CredentialWriter stores one provider API key in the auth broker. The broker
// client in internal/llmproxy implements it; tests can supply a fake.
type CredentialWriter interface {
	SetModelCredential(ctx context.Context, provider, key string) (masked string, err error)
}

type setModelCredentialInput struct {
	Provider string `json:"provider" jsonschema:"provider id, for example deepseek"`
	APIKey   string `json:"apiKey" jsonschema:"provider API key; the broker stores it and the platform never returns it"`
}

type setModelCredentialOutput struct {
	Provider   string `json:"provider"`
	Masked     string `json:"masked"`
	Configured bool   `json:"configured"`
}

func (ts *toolset) setModelCredential(ctx context.Context, _ *mcpsdk.CallToolRequest, in setModelCredentialInput) (*mcpsdk.CallToolResult, setModelCredentialOutput, error) {
	if ts.opts.Credentials == nil {
		return nil, setModelCredentialOutput{}, fmt.Errorf("model credentials are not configured on this platform")
	}
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if !llmproxy.ValidProvider(provider) {
		return nil, setModelCredentialOutput{}, fmt.Errorf("provider must be a lowercase id such as deepseek")
	}
	if err := llmproxy.ValidateAPIKey(in.APIKey); err != nil {
		// ValidateAPIKey never includes the key in its message.
		return nil, setModelCredentialOutput{}, err
	}
	masked, err := ts.opts.Credentials.SetModelCredential(ctx, provider, in.APIKey)
	if err != nil {
		return nil, setModelCredentialOutput{}, fmt.Errorf("could not store the model credential: %w", err)
	}
	return nil, setModelCredentialOutput{Provider: provider, Masked: masked, Configured: true}, nil
}
