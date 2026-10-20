package llmproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type fakeBackend struct {
	queenEnabled bool
	mu           sync.Mutex
	tokens       map[string]domain.Token
	agents       map[string]domain.Agent
	agentCost    map[string]float64
	hiveCost     map[string]float64
	usages       []domain.LLMUsage
	messages     []domain.Message
	inbox        map[string][]domain.Message
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		tokens: map[string]domain.Token{
			"agent-token": {ID: "token-agent", Scope: domain.TokenAgent, AgentID: "agent-1"},
			"user-token":  {ID: "token-user", Scope: domain.TokenDefault},
		},
		agents: map[string]domain.Agent{
			"agent-1": {ID: "agent-1", HiveID: "hive-1", Role: domain.RoleForager, ParentID: "queen-1", ChainID: "chain-1"},
			"queen-1": {ID: "queen-1", HiveID: "hive-1", Role: domain.RoleQueen},
		},
		agentCost: map[string]float64{},
		hiveCost:  map[string]float64{},
		inbox:     map[string][]domain.Message{},
	}
}

func (f *fakeBackend) Hive(_ context.Context, id string) (domain.Hive, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return domain.Hive{ID: id, QueenEnabled: f.queenEnabled}, nil
}

func (f *fakeBackend) TokenByPlaintext(_ context.Context, plaintext string) (domain.Token, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	token, ok := f.tokens[plaintext]
	if !ok {
		return domain.Token{}, domain.ErrNotFound
	}
	return token, nil
}

func (f *fakeBackend) Agent(_ context.Context, id string) (domain.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	agent, ok := f.agents[id]
	if !ok {
		return domain.Agent{}, domain.ErrNotFound
	}
	return agent, nil
}

func (f *fakeBackend) AgentCost(_ context.Context, agentID string) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.agentCost[agentID], nil
}

func (f *fakeBackend) HiveCostToday(_ context.Context, hiveID string) (float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hiveCost[hiveID], nil
}

func (f *fakeBackend) RecordLLMUsage(_ context.Context, usage domain.LLMUsage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.usages = append(f.usages, usage)
	return nil
}

func (f *fakeBackend) Inbox(_ context.Context, _ string, to domain.Address, _ string, _ int) ([]domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Message(nil), f.inbox[string(to)]...), nil
}

func (f *fakeBackend) SendMessage(_ context.Context, message domain.Message) (domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = append(f.messages, message)
	return message, nil
}

func (f *fakeBackend) usageCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.usages)
}

func (f *fakeBackend) lastUsage() domain.LLMUsage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.usages[len(f.usages)-1]
}

func (f *fakeBackend) messageCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.messages)
}

func waitForUsage(t *testing.T, backend *fakeBackend, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if backend.usageCount() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("usage rows = %d, want %d", backend.usageCount(), want)
}

func (f *fakeBackend) lastMessage() domain.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.messages[len(f.messages)-1]
}

func newTestProxy(t *testing.T, gatewayURL string, backend Backend, limits LimitsProvider, logger *slog.Logger) *httptest.Server {
	t.Helper()
	tokenPath := filepath.Join(t.TempDir(), "auth-gateway.token")
	if err := os.WriteFile(tokenPath, []byte("gateway-secret-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	proxy, err := New(Config{
		GatewayURL:       gatewayURL,
		GatewayTokenFile: tokenPath,
		Store:            backend,
		Limits:           limits,
		Logger:           logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(proxy)
	t.Cleanup(server.Close)
	return server
}

func agentRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer agent-token")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestProxyUsesAgentTokenAndGatewayToken(t *testing.T) {
	backend := newFakeBackend()
	var seenAuthorization, seenPath string
	gatewayCalls := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayCalls++
		seenAuthorization = r.Header.Get("Authorization")
		seenPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"deepseek/deepseek-chat","usage":{"prompt_tokens":1000,"completion_tokens":500}}`)
	}))
	defer gateway.Close()
	proxy := newTestProxy(t, gateway.URL, backend, NoLimits(), nil)

	req := agentRequest(t, http.MethodPost, proxy.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek/deepseek-chat","messages":[]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	if seenPath != "/v1/chat/completions" {
		t.Fatalf("gateway path = %q", seenPath)
	}
	if seenAuthorization != "Bearer gateway-secret-token" {
		t.Fatalf("gateway saw %q", seenAuthorization)
	}
	if strings.Contains(string(body), "gateway-secret-token") || strings.Contains(string(body), "agent-token") {
		t.Fatalf("response leaked a bearer token: %s", body)
	}
	waitForUsage(t, backend, 1)
	usage := backend.lastUsage()
	if usage.AgentID != "agent-1" || usage.HiveID != "hive-1" || usage.Model != "deepseek/deepseek-chat" {
		t.Fatalf("usage = %+v", usage)
	}
	if usage.InputTokens != 1000 || usage.OutputTokens != 500 || usage.CostUSD <= 0 || usage.UnknownPrice {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestProxyRejectsGatewayControlRoutes(t *testing.T) {
	backend := newFakeBackend()
	gatewayCalls := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayCalls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer gateway.Close()
	proxy := newTestProxy(t, gateway.URL, backend, NoLimits(), nil)

	for _, path := range []string{"/llm/v1/usage", "/llm/v1/credentials/check", "/llm/healthz"} {
		resp, err := http.DefaultClient.Do(agentRequest(t, http.MethodGet, proxy.URL+path, nil))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s = %d", path, resp.StatusCode)
		}
	}
	if gatewayCalls != 0 {
		t.Fatalf("gateway was called %d times", gatewayCalls)
	}
}

func TestProxyRejectsNonAgentAndRevokedTokens(t *testing.T) {
	backend := newFakeBackend()
	gatewayCalls := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayCalls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer gateway.Close()
	proxy := newTestProxy(t, gateway.URL, backend, NoLimits(), nil)

	req := agentRequest(t, http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer user-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("default token status = %d", resp.StatusCode)
	}

	// A token removed from the store is a revoked token.
	backend.mu.Lock()
	delete(backend.tokens, "agent-token")
	backend.mu.Unlock()
	resp, err = http.DefaultClient.Do(agentRequest(t, http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(`{}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d", resp.StatusCode)
	}
	if gatewayCalls != 0 {
		t.Fatalf("gateway was called %d times", gatewayCalls)
	}
}

func TestProxyStreamsResponseAndParsesPiUsage(t *testing.T) {
	backend := newFakeBackend()
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, `data: {"type":"text_delta","delta":"one"}`+"\n\n")
		flusher.Flush()
		<-release
		_, _ = io.WriteString(w, `data: {"type":"done","message":{"model":"deepseek/deepseek-chat","usage":{"input":100,"output":20}}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer gateway.Close()
	proxy := newTestProxy(t, gateway.URL, backend, NoLimits(), nil)

	req := agentRequest(t, http.MethodPost, proxy.URL+"/v1/pi/stream", strings.NewReader(`{"modelId":"deepseek/deepseek-chat"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	first := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		first <- line
	}()
	select {
	case line := <-first:
		if !strings.Contains(line, "text_delta") {
			t.Fatalf("first streamed line = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("proxy buffered the stream instead of forwarding it")
	}
	unblock()
	rest, _ := io.ReadAll(reader)
	if !strings.Contains(string(rest), `"type":"done"`) || !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("rest of stream = %q", rest)
	}
	waitForUsage(t, backend, 1)
	usage := backend.lastUsage()
	if usage.InputTokens != 100 || usage.OutputTokens != 20 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestProxyBudgetPerAgentNotifiesOnce(t *testing.T) {
	backend := newFakeBackend()
	backend.agentCost["agent-1"] = 1.5
	limits := LimitsFunc(func(context.Context, string) (domain.Limits, error) {
		return domain.Limits{MaxCostPerAgentUSD: 1}, nil
	})
	gatewayCalls := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayCalls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer gateway.Close()
	proxy := newTestProxy(t, gateway.URL, backend, limits, nil)

	for i := 0; i < 2; i++ {
		req := agentRequest(t, http.MethodPost, proxy.URL+"/v1/pi/stream", strings.NewReader(`{"modelId":"x"}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusPaymentRequired {
			t.Fatalf("status = %d: %s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), "maxCostPerAgentUsd") || !strings.Contains(string(body), "1.50") {
			t.Fatalf("budget error = %s", body)
		}
	}
	if gatewayCalls != 0 {
		t.Fatalf("gateway was called %d times", gatewayCalls)
	}
	if backend.messageCount() != 1 {
		t.Fatalf("system messages = %d", backend.messageCount())
	}
	message := backend.lastMessage()
	if message.From != domain.AddressSystem || message.To != domain.AddressQueen ||
		message.Trust != domain.TrustSystem || !strings.Contains(message.Body, "maxCostPerAgentUsd") {
		t.Fatalf("message = %+v", message)
	}
}

func TestProxyBudgetPerHiveDay(t *testing.T) {
	backend := newFakeBackend()
	backend.hiveCost["hive-1"] = 3
	limits := LimitsFunc(func(context.Context, string) (domain.Limits, error) {
		return domain.Limits{MaxCostPerDayUSD: 2}, nil
	})
	gatewayCalls := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayCalls++
		_, _ = io.WriteString(w, `{}`)
	}))
	defer gateway.Close()
	proxy := newTestProxy(t, gateway.URL, backend, limits, nil)

	req := agentRequest(t, http.MethodPost, proxy.URL+"/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired || !strings.Contains(string(body), "maxCostPerDayUsd") {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	if gatewayCalls != 0 {
		t.Fatalf("gateway was called %d times", gatewayCalls)
	}
	if backend.messageCount() != 1 {
		t.Fatalf("system messages = %d", backend.messageCount())
	}
}

func TestProxyUnknownModelIsZeroAndMarked(t *testing.T) {
	backend := newFakeBackend()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"model":"vendor/unknown","usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	}))
	defer gateway.Close()
	proxy := newTestProxy(t, gateway.URL, backend, NoLimits(), nil)

	req := agentRequest(t, http.MethodPost, proxy.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"vendor/unknown"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	waitForUsage(t, backend, 1)
	usage := backend.lastUsage()
	if usage.CostUSD != 0 || !usage.UnknownPrice {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestProxyLogsDoNotContainSecrets(t *testing.T) {
	backend := newFakeBackend()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	gatewayURL := gateway.URL
	gateway.Close() // force a transport error so the proxy logs

	var logBytes bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBytes, &slog.HandlerOptions{Level: slog.LevelDebug}))
	proxy := newTestProxy(t, gatewayURL, backend, NoLimits(), logger)

	secretKey := "sk-test-not-a-real-key-123456"
	req := agentRequest(t, http.MethodPost, proxy.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"deepseek/deepseek-chat","apiKey":"`+secretKey+`"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	logs := logBytes.String()
	for _, secret := range []string{"agent-token", "gateway-secret-token", secretKey} {
		if strings.Contains(logs, secret) {
			t.Fatalf("logs contain %q: %s", secret, logs)
		}
	}
}

func TestMaskSecretNeverReturnsTheSecret(t *testing.T) {
	for _, secret := range []string{"short", "sk-abcdefgh", "sk-live-very-secret-value"} {
		masked := MaskSecret(secret)
		if masked == secret || strings.Contains(masked, secret) {
			t.Fatalf("MaskSecret(%q) = %q", secret, masked)
		}
	}
}

func TestRequestModelShapes(t *testing.T) {
	cases := map[string]string{
		`{"model":"deepseek/deepseek-chat"}`:       "deepseek/deepseek-chat",
		`{"modelId":"deepseek/deepseek-reasoner"}`: "deepseek/deepseek-reasoner",
		`{"model":{"id":"openai/gpt-4o-mini"}}`:    "openai/gpt-4o-mini",
		`not-json`:                                 "",
	}
	for body, want := range cases {
		if got := requestModel([]byte(body)); got != want {
			t.Fatalf("requestModel(%s) = %q, want %q", body, got, want)
		}
	}
}

func TestUsageEnvelopeJSON(t *testing.T) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4}}`), &payload); err != nil {
		t.Fatal(err)
	}
	usage, ok := ExtractUsage([]byte(`{"usage":{"prompt_tokens":3,"completion_tokens":4}}`))
	if !ok || usage.Input != 3 || usage.Output != 4 {
		t.Fatalf("usage = %+v ok=%v", usage, ok)
	}
}
