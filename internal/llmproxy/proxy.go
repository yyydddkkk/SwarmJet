package llmproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// Backend is the storage surface the proxy needs. *store.Store satisfies it.
type Backend interface {
	TokenByPlaintext(ctx context.Context, plaintext string) (domain.Token, error)
	Agent(ctx context.Context, id string) (domain.Agent, error)
	AgentCost(ctx context.Context, agentID string) (float64, error)
	HiveCostToday(ctx context.Context, hiveID string) (float64, error)
	RecordLLMUsage(ctx context.Context, usage domain.LLMUsage) error
	Inbox(ctx context.Context, hiveID string, to domain.Address, afterID string, limit int) ([]domain.Message, error)
	SendMessage(ctx context.Context, m domain.Message) (domain.Message, error)
}

// LimitsProvider resolves the active methodology's limits. Task C supplies the
// real store implementation; NoLimits is the stand-in until then.
type LimitsProvider interface {
	Limits(ctx context.Context, hiveID string) (domain.Limits, error)
}

// LimitsFunc adapts a function to LimitsProvider.
type LimitsFunc func(context.Context, string) (domain.Limits, error)

func (f LimitsFunc) Limits(ctx context.Context, hiveID string) (domain.Limits, error) {
	return f(ctx, hiveID)
}

// NoLimits keeps the proxy usable before the methodology store lands.
func NoLimits() LimitsProvider {
	return LimitsFunc(func(context.Context, string) (domain.Limits, error) { return domain.Limits{}, nil })
}

// MethodologyReader is the store contract implemented by task C. The cmd wiring
// uses a runtime assertion so this branch compiles without that method.
type MethodologyReader interface {
	ActiveMethodology(ctx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error)
}

// StoreLimits reads limits from the Hive's active methodology.
type StoreLimits struct {
	Reader MethodologyReader
}

func (s StoreLimits) Limits(ctx context.Context, hiveID string) (domain.Limits, error) {
	methodology, _, err := s.Reader.ActiveMethodology(ctx, hiveID)
	if err != nil {
		return domain.Limits{}, err
	}
	return methodology.Manifest.Limits, nil
}

// Config configures the /llm proxy.
type Config struct {
	GatewayURL       string
	GatewayTokenFile string
	Store            Backend
	Limits           LimitsProvider
	Logger           *slog.Logger
	HTTPClient       *http.Client
	Now              func() time.Time
	// MaxRequestBytes caps a buffered prompt body. Defaults to 64 MiB.
	MaxRequestBytes int64
}

// Proxy authenticates Agent tokens and forwards model requests to the auth
// gateway with the gateway's own bearer token.
type Proxy struct {
	base             *url.URL
	gatewayTokenFile string
	store            Backend
	limits           LimitsProvider
	logger           *slog.Logger
	client           *http.Client
	now              func() time.Time
	maxRequestBytes  int64

	notified sync.Map // "agentID|limit" -> struct{}
}

// New builds the proxy. A missing token file is not fatal here: /llm answers
// 503 until deploy/install.py writes the token.
func New(cfg Config) (*Proxy, error) {
	raw := strings.TrimRight(strings.TrimSpace(cfg.GatewayURL), "/")
	if raw == "" {
		return nil, ErrNotConfigured
	}
	base, err := url.Parse(raw)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return nil, errors.New("llmproxy: gateway URL must be an http(s) URL")
	}
	if cfg.Store == nil {
		return nil, errors.New("llmproxy: nil store")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	client := cfg.HTTPClient
	if client == nil {
		transport := &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 5 * time.Minute,
			ExpectContinueTimeout: time.Second,
		}
		client = &http.Client{Transport: transport}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	maxBytes := cfg.MaxRequestBytes
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	limits := cfg.Limits
	if limits == nil {
		limits = NoLimits()
	}
	return &Proxy{
		base:             base,
		gatewayTokenFile: cfg.GatewayTokenFile,
		store:            cfg.Store,
		limits:           limits,
		logger:           logger,
		client:           client,
		now:              now,
		maxRequestBytes:  maxBytes,
	}, nil
}

// ServeHTTP implements the /llm endpoint. It deliberately accepts every path
// below /llm so OpenAI, Anthropic, Responses and pi-native routes all pass
// through unchanged.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || len(token) > 1024 {
		writeError(w, http.StatusUnauthorized, "agent token required")
		return
	}
	record, err := p.store.TokenByPlaintext(r.Context(), token)
	if err != nil {
		p.logger.Debug("llmproxy: rejected token", "remote", remoteHost(r.RemoteAddr))
		writeError(w, http.StatusUnauthorized, "invalid agent token")
		return
	}
	if record.Scope != domain.TokenAgent || record.AgentID == "" {
		writeError(w, http.StatusForbidden, "an agent-scoped token is required")
		return
	}
	agent, err := p.store.Agent(r.Context(), record.AgentID)
	if err != nil {
		p.logger.Debug("llmproxy: agent token does not resolve", "agent", record.AgentID)
		writeError(w, http.StatusUnauthorized, "invalid agent token")
		return
	}
	if exceeded := p.checkBudget(r.Context(), agent); exceeded != nil {
		p.notifyBudget(r.Context(), agent, exceeded)
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"error":      "budget exceeded",
			"limit":      exceeded.name,
			"limitUsd":   exceeded.limit,
			"currentUsd": exceeded.current,
			"agentId":    agent.ID,
			"message": fmt.Sprintf("%s exceeded: limit %.2f USD, current spend %.2f USD",
				exceeded.name, exceeded.limit, exceeded.current),
		})
		return
	}

	gatewayToken, err := readSecretFile(p.gatewayTokenFile)
	if err != nil {
		p.logger.Warn("llmproxy: gateway token unavailable", "error", err)
		writeError(w, http.StatusServiceUnavailable, "model gateway is not configured")
		return
	}

	var body []byte
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, p.maxRequestBytes+1))
		_ = r.Body.Close()
		if err != nil {
			writeError(w, http.StatusBadRequest, "could not read request body")
			return
		}
		if int64(len(body)) > p.maxRequestBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
	}
	model := requestModel(body)
	upstreamRoute := upstreamPath(r.URL.Path)
	if !allowedGatewayPath(upstreamRoute) {
		writeError(w, http.StatusNotFound, "model route not found")
		return
	}

	target := *p.base
	target.Path = strings.TrimRight(p.base.Path, "/") + upstreamRoute
	target.RawQuery = r.URL.RawQuery

	upstream, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not build the gateway request")
		return
	}
	copyRequestHeaders(upstream.Header, r.Header)
	upstream.Header.Set("Authorization", "Bearer "+gatewayToken)
	upstream.Header.Set("X-OMP-App", "swarmjet")
	upstream.Header.Set("X-OMP-Install-Id", agent.ID)
	upstream.Header.Set("X-OMP-Hostname", "agent-"+agent.ID)

	response, err := p.client.Do(upstream)
	if err != nil {
		p.logger.Warn("llmproxy: gateway request failed", "agent", agent.ID, "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusBadGateway, "model gateway request failed")
		return
	}
	defer response.Body.Close()

	removeHopByHop(response.Header)
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	capture := &tailBuffer{max: 1 << 20}
	_, copyErr := copyResponse(w, response.Body, capture)
	if copyErr != nil && !errors.Is(copyErr, context.Canceled) {
		p.logger.Debug("llmproxy: response copy ended", "agent", agent.ID, "error", copyErr)
	}

	// Record even when the client disconnected after the gateway reported
	// usage; the request may already have been billed upstream.
	p.recordUsage(r.Context(), agent, model, capture.Bytes())
}

type budgetExceeded struct {
	name    string
	limit   float64
	current float64
}

func (p *Proxy) checkBudget(ctx context.Context, agent domain.Agent) *budgetExceeded {
	limits, err := p.limits.Limits(ctx, agent.HiveID)
	if err != nil {
		if !errors.Is(err, domain.ErrNotFound) {
			p.logger.Warn("llmproxy: could not read methodology limits", "hive", agent.HiveID, "error", err)
		}
		return nil
	}
	if limits.MaxCostPerAgentUSD > 0 {
		current, err := p.store.AgentCost(ctx, agent.ID)
		if err != nil {
			p.logger.Warn("llmproxy: could not read agent cost", "agent", agent.ID, "error", err)
		} else if current >= limits.MaxCostPerAgentUSD {
			return &budgetExceeded{name: "maxCostPerAgentUsd", limit: limits.MaxCostPerAgentUSD, current: current}
		}
	}
	if limits.MaxCostPerDayUSD > 0 {
		current, err := p.store.HiveCostToday(ctx, agent.HiveID)
		if err != nil {
			p.logger.Warn("llmproxy: could not read hive cost", "hive", agent.HiveID, "error", err)
		} else if current >= limits.MaxCostPerDayUSD {
			return &budgetExceeded{name: "maxCostPerDayUsd", limit: limits.MaxCostPerDayUSD, current: current}
		}
	}
	return nil
}

// notifyBudget sends one system message per Agent and limit. The dispatcher is
// the Agent's parent when known, otherwise the sender of its latest task.
func (p *Proxy) notifyBudget(ctx context.Context, agent domain.Agent, exceeded *budgetExceeded) {
	key := agent.ID + "|" + exceeded.name
	if _, loaded := p.notified.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	to := p.dispatcher(ctx, agent)
	if to == "" {
		return
	}
	body := fmt.Sprintf("Model budget limit %s reached for agent %s: limit %.2f USD, current spend %.2f USD. Its model requests are paused until the budget is raised.",
		exceeded.name, agent.ID, exceeded.limit, exceeded.current)
	message := domain.Message{
		HiveID:  agent.HiveID,
		From:    domain.AddressSystem,
		To:      to,
		Kind:    domain.MessageEvent,
		ChainID: agent.ChainID,
		Body:    body,
		Trust:   domain.TrustSystem,
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := p.store.SendMessage(sendCtx, message); err != nil {
		p.logger.Warn("llmproxy: could not notify dispatcher", "agent", agent.ID, "limit", exceeded.name, "error", err)
	}
}

func (p *Proxy) dispatcher(ctx context.Context, agent domain.Agent) domain.Address {
	if agent.ParentID != "" {
		if parent, err := p.store.Agent(ctx, agent.ParentID); err == nil {
			if parent.Role == domain.RoleQueen {
				return domain.AddressQueen
			}
			return domain.ForagerAddress(parent.ID)
		}
	}
	to := domain.ForagerAddress(agent.ID)
	if agent.Role == domain.RoleQueen {
		to = domain.AddressQueen
	}
	messages, err := p.store.Inbox(ctx, agent.HiveID, to, "", 100)
	if err == nil {
		for i := len(messages) - 1; i >= 0; i-- {
			message := messages[i]
			if message.Kind != domain.MessageTask || message.From == "" {
				continue
			}
			if message.From == domain.ForagerAddress(agent.ID) {
				continue
			}
			return message.From
		}
	}
	return domain.AddressHive
}

func (p *Proxy) recordUsage(ctx context.Context, agent domain.Agent, model string, body []byte) {
	usage, ok := ExtractUsage(body)
	if !ok {
		return
	}
	if model == "" {
		model = usage.Model
	}
	if model == "" {
		model = "unknown"
	}
	cost, known := EstimateCost(model, usage)
	record := domain.LLMUsage{
		AgentID:          agent.ID,
		HiveID:           agent.HiveID,
		Model:            model,
		InputTokens:      usage.Input,
		OutputTokens:     usage.Output,
		CacheReadTokens:  usage.CacheRead,
		CacheWriteTokens: usage.CacheWrite,
		CostUSD:          cost,
		UnknownPrice:     !known,
		CreatedAt:        p.now().UTC(),
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := p.store.RecordLLMUsage(recordCtx, record); err != nil {
		p.logger.Warn("llmproxy: could not record usage", "agent", agent.ID, "error", err)
	}
}

func upstreamPath(path string) string {
	path = strings.TrimPrefix(path, "/llm")
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

// allowedGatewayPath keeps the proxy to model traffic. Gateway-wide endpoints
// such as /v1/usage and /v1/credentials/check stay reachable only by the
// platform itself, not by an Agent token.
func allowedGatewayPath(path string) bool {
	switch path {
	case "/v1/chat/completions", "/v1/messages", "/v1/responses", "/v1/pi/stream",
		"/v1/systemone", "/v1/models", "/v1/embeddings", "/v1/rerank":
		return true
	}
	for _, prefix := range []string{"/v1/images", "/v1/audio", "/v1/videos"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}

func remoteHost(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	return host
}

var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

// removeHopByHop also removes headers named by the Connection header.
func removeHopByHop(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			if name = strings.TrimSpace(name); name != "" {
				header.Del(name)
			}
		}
	}
	for name := range hopByHopHeaders {
		header.Del(name)
	}
}

func copyRequestHeaders(dst, src http.Header) {
	for key, values := range src {
		switch http.CanonicalHeaderKey(key) {
		case "Host", "Authorization", "Cookie", "Accept-Encoding", "Content-Length",
			"Api-Key", "X-Api-Key", "X-Goog-Api-Key", "X-Forwarded-Authorization":
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
	removeHopByHop(dst)
}

// tailBuffer keeps the last max bytes of a stream so usage can be parsed after
// the response has already been forwarded.
type tailBuffer struct {
	max  int
	data []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	if b.max > 0 && len(b.data) > b.max {
		b.data = append([]byte(nil), b.data[len(b.data)-b.max:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) Bytes() []byte { return b.data }

func copyResponse(w http.ResponseWriter, body io.Reader, capture *tailBuffer) (int64, error) {
	controller := http.NewResponseController(w)
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		n, err := body.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			_, _ = capture.Write(chunk)
			written, writeErr := w.Write(chunk)
			total += int64(written)
			_ = controller.Flush()
			if writeErr != nil {
				return total, writeErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return total, nil
			}
			return total, err
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
