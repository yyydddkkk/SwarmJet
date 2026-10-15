package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/workspace"
)

type staticLookup map[string]string

func (m staticLookup) NodeAddress(_ context.Context, id string) (string, error) {
	address, ok := m[id]
	if !ok {
		return "", errors.New("unknown node")
	}
	return address, nil
}

func rpcBody(t *testing.T, method string, params any) []byte {
	t.Helper()
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		raw = b
	}
	body, err := json.Marshal(rpcRequest{Method: method, Params: raw})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func fixedRequestID() string { return strings.Repeat("a", 32) }

// fakeNode embeds the interface and overrides only what the transport tests
// exercise; any unexpected call panics on the nil embedded value.
type fakeNode struct {
	Node
	mu          sync.Mutex
	prepared    []string
	started     []domain.AgentRequest
	inspections int
	reads       int
	report      []byte
	reportErr   error
}

func (f *fakeNode) ID() string { return "fake" }
func (f *fakeNode) PrepareWorkspace(_ context.Context, taskID string, _ domain.Project, _ map[string]string) (domain.Workspace, error) {
	f.mu.Lock()
	f.prepared = append(f.prepared, taskID)
	f.mu.Unlock()
	return domain.Workspace{TaskID: taskID, Root: "/node/" + taskID}, nil
}
func (f *fakeNode) Start(_ context.Context, req domain.AgentRequest) (domain.Execution, error) {
	f.mu.Lock()
	f.started = append(f.started, req)
	f.mu.Unlock()
	return domain.Execution{ID: req.ExecutionID, Status: "succeeded", Result: &domain.AgentResult{Outcome: "changed", Summary: "ok"}}, nil
}
func (f *fakeNode) Inspect(_ context.Context, id string) (domain.Execution, error) {
	f.mu.Lock()
	f.inspections++
	f.mu.Unlock()
	return domain.Execution{ID: id, Status: "running"}, nil
}
func (f *fakeNode) ReadReport(context.Context, string, string) ([]byte, error) {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
	if f.reportErr != nil {
		return nil, f.reportErr
	}
	return f.report, nil
}
func (f *fakeNode) Cancel(context.Context, string) error { return errors.New("node failure") }

func (f *fakeNode) inspectionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inspections
}
func (f *fakeNode) readCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}
func (f *fakeNode) startedRequests() []domain.AgentRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.AgentRequest, len(f.started))
	copy(out, f.started)
	return out
}

func TestVerifyNodeRequestBindsTimestampRequestIDAndBody(t *testing.T) {
	secret := strings.Repeat("s", 32)
	id := fixedRequestID()
	body := rpcBody(t, "agentInspect", map[string]any{"executionId": "x"})
	other := rpcBody(t, "agentCancel", map[string]any{"executionId": "x"})
	now := time.Now().UTC()
	signature := signNodeRequest(secret, now.Unix(), id, body)
	if !VerifyNodeRequest(secret, strconv.FormatInt(now.Unix(), 10), id, signature, body, now) {
		t.Fatal("valid signature rejected")
	}
	if VerifyNodeRequest(secret, strconv.FormatInt(now.Unix(), 10), id, signature, other, now) {
		t.Fatal("tampered body accepted")
	}
	if VerifyNodeRequest(secret, strconv.FormatInt(now.Unix(), 10), strings.Repeat("b", 32), signature, body, now) {
		t.Fatal("signature accepted for a different request id")
	}
	stale := now.Add(-5 * time.Minute)
	if VerifyNodeRequest(secret, strconv.FormatInt(stale.Unix(), 10), id, signNodeRequest(secret, stale.Unix(), id, body), body, now) {
		t.Fatal("stale timestamp accepted")
	}
	if VerifyNodeRequest("other", strconv.FormatInt(now.Unix(), 10), id, signature, body, now) {
		t.Fatal("wrong secret accepted")
	}
	if VerifyNodeRequest(secret, strconv.FormatInt(now.Unix(), 10), "", signature, body, now) {
		t.Fatal("empty request id accepted")
	}
	if VerifyNodeRequest("", strconv.FormatInt(now.Unix(), 10), id, signature, body, now) {
		t.Fatal("empty secret accepted")
	}
}

func TestHandlerReturnsCachedResultForRepeatedRequestID(t *testing.T) {
	secret := strings.Repeat("s", 32)
	fake := &fakeNode{}
	h := &Handler{Node: fake, Secret: secret}
	body := rpcBody(t, "agentInspect", map[string]any{"executionId": "x"})
	now := time.Now().UTC()
	id := fixedRequestID()
	signature := signNodeRequest(secret, now.Unix(), id, body)
	send := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(body))
		r.Header.Set(HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
		r.Header.Set(HeaderRequestID, id)
		r.Header.Set(HeaderSignature, signature)
		h.ServeHTTP(w, r)
		return w
	}
	first, second := send(), send()
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("status first=%d second=%d", first.Code, second.Code)
	}
	if first.Body.String() != second.Body.String() || !strings.Contains(first.Body.String(), "running") {
		t.Fatalf("cached response mismatch: %s / %s", first.Body.String(), second.Body.String())
	}
	if fake.inspectionCount() != 1 {
		t.Fatalf("repeated request dispatched %d times", fake.inspectionCount())
	}
}

func TestRemoteNodeRoundTripDeliversCredentials(t *testing.T) {
	secret := strings.Repeat("s", 32)
	fake := &fakeNode{report: []byte("log body")}
	srv := httptest.NewServer(&Handler{Node: fake, Secret: secret})
	defer srv.Close()
	pool := &Pool{Lookup: staticLookup{"node-a": srv.URL}, Secret: secret}
	node, err := pool.Node(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ws, err := node.PrepareWorkspace(ctx, "task-a", domain.Project{ID: "p"}, map[string]string{"GITLAB_TOKEN": "secret-token"})
	if err != nil || ws.Root != "/node/task-a" {
		t.Fatalf("workspace=%+v err=%v", ws, err)
	}
	ex, err := node.Start(ctx, domain.AgentRequest{ExecutionID: "task-a-dev-1", TaskID: "task-a", Role: "dev", Workspace: ws.Root, ReadOnlyWorkspace: true, Env: map[string]string{"GITLAB_TOKEN": "secret-token", "GIT_ASKPASS": "/askpass"}})
	if err != nil || ex.Status != "succeeded" || ex.Result == nil || ex.Result.Outcome != "changed" {
		t.Fatalf("execution=%+v err=%v", ex, err)
	}
	started := fake.startedRequests()
	if len(started) != 1 {
		t.Fatalf("agent started %d times", len(started))
	}
	if !started[0].ReadOnlyWorkspace {
		t.Fatal("remote node lost result recovery's read-only source mount")
	}
	got := started[0].Env
	if got["GITLAB_TOKEN"] != "secret-token" || got["GIT_ASKPASS"] != "/askpass" {
		t.Fatalf("credentials not delivered: %+v", got)
	}
	body, err := node.ReadReport(ctx, "task-a", "log.txt")
	if err != nil || string(body) != "log body" {
		t.Fatalf("report=%q err=%v", body, err)
	}
	if err := node.Cancel(ctx, "task-a-dev-1"); err == nil || !strings.Contains(err.Error(), "node failure") {
		t.Fatalf("remote error not propagated: %v", err)
	}
}

func TestAgentWireKeepsMergeDevelopmentScope(t *testing.T) {
	req := domain.AgentRequest{ExecutionID: "merge", TaskID: "task", Role: "merge", SessionID: "dev-session", ProfileScope: "dev"}
	got := wireAgent(req).request()
	if got.SessionID != req.SessionID || got.ProfileScope != "dev" || got.Role != "merge" {
		t.Fatalf("remote merge lost development context: %+v", got)
	}
}

func TestRemoteCallRefusesRedirect(t *testing.T) {
	secret := strings.Repeat("s", 32)
	var hits int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer attacker.Close()
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+"/execute", http.StatusTemporaryRedirect)
	}))
	defer node.Close()
	pool := &Pool{Lookup: staticLookup{"node-a": node.URL}, Secret: secret}
	n, err := pool.Node(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = n.Inspect(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "refused redirect") {
		t.Fatalf("redirect error=%v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("redirect target received %d requests", got)
	}
}

func TestRemoteErrorPreservesNeedsSync(t *testing.T) {
	secret := strings.Repeat("s", 32)
	fake := &fakeNode{reportErr: workspace.ErrNeedsSync}
	srv := httptest.NewServer(&Handler{Node: fake, Secret: secret})
	defer srv.Close()
	pool := &Pool{Lookup: staticLookup{"node-a": srv.URL}, Secret: secret}
	n, err := pool.Node(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = n.ReadReport(context.Background(), "task-a", "log.txt"); !errors.Is(err, workspace.ErrNeedsSync) {
		t.Fatalf("typed error lost: %v", err)
	}
}

func TestNodeCallTimeoutBoundsStall(t *testing.T) {
	secret := strings.Repeat("s", 32)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bounded so Server.Close cannot block; the client must return earlier.
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()
	old := nodeTimeouts["agentInspect"]
	nodeTimeouts["agentInspect"] = 100 * time.Millisecond
	t.Cleanup(func() { nodeTimeouts["agentInspect"] = old })
	pool := &Pool{Lookup: staticLookup{"node-a": srv.URL}, Secret: secret}
	n, err := pool.Node(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err = n.Inspect(context.Background(), "x"); err == nil {
		t.Fatal("stalled call did not time out")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestHandlerRejectsUnauthenticatedRequests(t *testing.T) {
	secret := strings.Repeat("s", 32)
	h := &Handler{Node: &fakeNode{}, Secret: secret}
	body := rpcBody(t, "agentInspect", map[string]any{"executionId": "x"})
	tampered := rpcBody(t, "agentCancel", map[string]any{"executionId": "x"})
	id := fixedRequestID()
	send := func(timestamp, requestID, signature string, payload []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(payload))
		if timestamp != "" {
			r.Header.Set(HeaderTimestamp, timestamp)
		}
		if requestID != "" {
			r.Header.Set(HeaderRequestID, requestID)
		}
		if signature != "" {
			r.Header.Set(HeaderSignature, signature)
		}
		h.ServeHTTP(w, r)
		return w
	}
	now := time.Now().UTC()
	valid := signNodeRequest(secret, now.Unix(), id, body)
	if w := send("", "", "", body); w.Code != http.StatusBadRequest {
		t.Fatalf("missing request id status=%d", w.Code)
	}
	if w := send(strconv.FormatInt(now.Unix(), 10), id, "", body); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing signature status=%d", w.Code)
	}
	if w := send(strconv.FormatInt(now.Unix(), 10), id, signNodeRequest("wrong", now.Unix(), id, body), body); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret status=%d", w.Code)
	}
	stale := now.Add(-time.Hour)
	if w := send(strconv.FormatInt(stale.Unix(), 10), id, signNodeRequest(secret, stale.Unix(), id, body), body); w.Code != http.StatusUnauthorized {
		t.Fatalf("stale status=%d", w.Code)
	}
	if w := send(strconv.FormatInt(now.Unix(), 10), id, valid, tampered); w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered status=%d", w.Code)
	}
	if w := send(strconv.FormatInt(now.Unix(), 10), strings.Repeat("b", 32), valid, body); w.Code != http.StatusUnauthorized {
		t.Fatalf("request id swap status=%d", w.Code)
	}
	if w := send(strconv.FormatInt(now.Unix(), 10), id, valid, body); w.Code != http.StatusOK {
		t.Fatalf("valid request status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestHandlerShapeAndUnknownMethod(t *testing.T) {
	secret := strings.Repeat("s", 32)
	h := &Handler{Node: &fakeNode{}, Secret: secret}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/execute", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/other", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown path status=%d", w.Code)
	}
	w = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader([]byte("{}")))
	r.Header.Set(HeaderRequestID, fixedRequestID())
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing signature status=%d", w.Code)
	}
	body := rpcBody(t, "nope", nil)
	now := time.Now().UTC()
	req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(body))
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	req.Header.Set(HeaderRequestID, fixedRequestID())
	req.Header.Set(HeaderSignature, signNodeRequest(secret, now.Unix(), fixedRequestID(), body))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "unknown execution method") {
		t.Fatalf("unknown method status=%d body=%s", w.Code, w.Body.String())
	}
}

// dirAgent writes only inside the node's own DATA_DIR, so a routing mistake is
// visible as a file in the wrong directory.
type dirAgent struct{ dataDir string }

func (d dirAgent) Start(_ context.Context, req domain.AgentRequest) (domain.Execution, error) {
	dir := filepath.Join(d.dataDir, "runs", req.TaskID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return domain.Execution{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.txt"), []byte(req.ExecutionID), 0600); err != nil {
		return domain.Execution{}, err
	}
	return domain.Execution{ID: req.ExecutionID, Status: "succeeded"}, nil
}
func (d dirAgent) Inspect(context.Context, string) (domain.Execution, error) {
	return domain.Execution{}, nil
}
func (d dirAgent) Cancel(context.Context, string) error     { return nil }
func (d dirAgent) CancelTask(context.Context, string) error { return nil }
func (d dirAgent) TaskExecutions(context.Context, string) ([]domain.Execution, error) {
	return nil, nil
}

func TestTwoNodesKeepTaskFilesSeparate(t *testing.T) {
	secret := strings.Repeat("s", 32)
	dirA, dirB := t.TempDir(), t.TempDir()
	a := &LocalNode{NodeID: "a", AgentBackend: dirAgent{dirA}, DataDir: dirA}
	b := &LocalNode{NodeID: "b", AgentBackend: dirAgent{dirB}, DataDir: dirB}
	sa := httptest.NewServer(&Handler{Node: a, Secret: secret})
	defer sa.Close()
	sb := httptest.NewServer(&Handler{Node: b, Secret: secret})
	defer sb.Close()
	pool := &Pool{Lookup: staticLookup{"a": sa.URL, "b": sb.URL}, Secret: secret}
	ctx := context.Background()
	na, err := pool.Node(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := pool.Node(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := na.Start(ctx, domain.AgentRequest{ExecutionID: "task-a-dev-1", TaskID: "task-a", Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nb.Start(ctx, domain.AgentRequest{ExecutionID: "task-b-dev-1", TaskID: "task-b", Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(dirA, "runs", "task-a", "agent.txt"), filepath.Join(dirB, "runs", "task-b", "agent.txt")} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dirA, "runs", "task-b")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("node a saw task b files: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirB, "runs", "task-a")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("node b saw task a files: %v", err)
	}
}

// gateNode blocks the first Inspect so a concurrent duplicate can be observed.
type gateNode struct {
	fakeNode
	entered chan struct{}
	release chan struct{}
}

func (g *gateNode) Inspect(_ context.Context, id string) (domain.Execution, error) {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
	g.mu.Lock()
	g.inspections++
	g.mu.Unlock()
	return domain.Execution{ID: id, Status: "running"}, nil
}

func TestConcurrentDuplicateWaitsForSingleExecution(t *testing.T) {
	secret := strings.Repeat("s", 32)
	gate := &gateNode{entered: make(chan struct{}, 4), release: make(chan struct{})}
	h := &Handler{Node: gate, Secret: secret}
	body := rpcBody(t, "agentInspect", map[string]any{"executionId": "x"})
	now := time.Now().UTC()
	id := fixedRequestID()
	signature := signNodeRequest(secret, now.Unix(), id, body)
	send := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(body))
		r.Header.Set(HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
		r.Header.Set(HeaderRequestID, id)
		r.Header.Set(HeaderSignature, signature)
		h.ServeHTTP(w, r)
		return w
	}
	results := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = send()
		}(i)
	}
	<-gate.entered
	close(gate.release)
	wg.Wait()
	if gate.inspectionCount() != 1 {
		t.Fatalf("concurrent duplicate executed %d times", gate.inspectionCount())
	}
	if results[0].Body.String() != results[1].Body.String() {
		t.Fatalf("responses differ: %s / %s", results[0].Body.String(), results[1].Body.String())
	}
}

// fakeClock lets a test advance the client and server view of time together.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// loseFirstResponse executes the first request but discards the response, as if
// the network dropped it after the node had already run the operation. When
// clock is set, the elapsed request time is simulated too.
type loseFirstResponse struct {
	base  http.RoundTripper
	clock *fakeClock
	n     int32
}

func (t *loseFirstResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	if atomic.AddInt32(&t.n, 1) == 1 {
		resp, err := t.base.RoundTrip(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		if t.clock != nil {
			t.clock.advance(5 * time.Minute)
		}
		return nil, errors.New("simulated lost response")
	}
	return t.base.RoundTrip(req)
}

func TestRetryAfterBodyTTLStillDeduped(t *testing.T) {
	secret := strings.Repeat("s", 32)
	clock := &fakeClock{t: time.Now().UTC()}
	old := nodeTimeouts["agentInspect"]
	nodeTimeouts["agentInspect"] = 30 * time.Minute
	t.Cleanup(func() { nodeTimeouts["agentInspect"] = old })
	fake := &fakeNode{}
	srv := httptest.NewServer(&Handler{Node: fake, Secret: secret, Now: clock.now})
	defer srv.Close()
	pool := &Pool{Lookup: staticLookup{"node-a": srv.URL}, Secret: secret, Now: clock.now, HTTP: &http.Client{Transport: &loseFirstResponse{base: http.DefaultTransport, clock: clock}}}
	n, err := pool.Node(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	// The call deadline is 30 minutes; the response body TTL (4 minutes) has
	// passed, but the dedup marker must still prevent a second execution.
	if _, err = n.Inspect(context.Background(), "x"); !errors.Is(err, ErrReplayUnavailable) {
		t.Fatalf("expected replay_unavailable, got %v", err)
	}
	if fake.inspectionCount() != 1 {
		t.Fatalf("retry after body TTL executed %d times", fake.inspectionCount())
	}
}

func TestTransportRetryReusesRequestIdentity(t *testing.T) {
	secret := strings.Repeat("s", 32)
	fake := &fakeNode{}
	srv := httptest.NewServer(&Handler{Node: fake, Secret: secret})
	defer srv.Close()
	pool := &Pool{Lookup: staticLookup{"node-a": srv.URL}, Secret: secret, HTTP: &http.Client{Transport: &loseFirstResponse{base: http.DefaultTransport}}}
	n, err := pool.Node(context.Background(), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = n.Inspect(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if fake.inspectionCount() != 1 {
		t.Fatalf("lost-response retry executed %d times", fake.inspectionCount())
	}
	// A separate logical call gets a new identity and executes again.
	if _, err = n.Inspect(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if fake.inspectionCount() != 2 {
		t.Fatalf("new call reused identity: %d", fake.inspectionCount())
	}
}

func TestReplayCacheFullRejectsInsteadOfEvicting(t *testing.T) {
	secret := strings.Repeat("s", 32)
	oldMax := replayMaxEntries
	replayMaxEntries = 2
	t.Cleanup(func() { replayMaxEntries = oldMax })
	fake := &fakeNode{}
	h := &Handler{Node: fake, Secret: secret}
	now := time.Now().UTC()
	send := func(id string) *httptest.ResponseRecorder {
		body := rpcBody(t, "agentInspect", map[string]any{"executionId": id})
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(body))
		r.Header.Set(HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
		r.Header.Set(HeaderRequestID, id)
		r.Header.Set(HeaderSignature, signNodeRequest(secret, now.Unix(), id, body))
		h.ServeHTTP(w, r)
		return w
	}
	idA, idB, idC := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	if w := send(idA); w.Code != http.StatusOK {
		t.Fatalf("first status=%d", w.Code)
	}
	if w := send(idB); w.Code != http.StatusOK {
		t.Fatalf("second status=%d", w.Code)
	}
	if w := send(idC); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("full cache status=%d", w.Code)
	}
	// The recorded ID must still dedup; it was not evicted to admit idC.
	if w := send(idA); w.Code != http.StatusOK {
		t.Fatalf("dedup status=%d", w.Code)
	}
	if fake.inspectionCount() != 2 {
		t.Fatalf("inspection count=%d", fake.inspectionCount())
	}
}

func TestLargeResponseKeepsExecutionMarker(t *testing.T) {
	secret := strings.Repeat("s", 32)
	oldBody := replayMaxBody
	replayMaxBody = 16
	t.Cleanup(func() { replayMaxBody = oldBody })
	fake := &fakeNode{report: bytes.Repeat([]byte("x"), 256)}
	h := &Handler{Node: fake, Secret: secret}
	now := time.Now().UTC()
	id := fixedRequestID()
	body := rpcBody(t, "readReport", map[string]any{"taskId": "t", "relative": "log.txt"})
	signature := signNodeRequest(secret, now.Unix(), id, body)
	send := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(body))
		r.Header.Set(HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
		r.Header.Set(HeaderRequestID, id)
		r.Header.Set(HeaderSignature, signature)
		h.ServeHTTP(w, r)
		return w
	}
	first := send()
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "result") {
		t.Fatalf("first=%d %s", first.Code, first.Body.String())
	}
	second := send()
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), codeReplayUnavailable) {
		t.Fatalf("second=%d %s", second.Code, second.Body.String())
	}
	if fake.readCount() != 1 {
		t.Fatalf("large response re-executed: %d", fake.readCount())
	}
}

// sweepWriter runs a hook exactly when the handler commits the status code,
// which is between the cached-body check and the body write.
type sweepWriter struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	onHeader func()
}

func (w *sweepWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *sweepWriter) WriteHeader(status int) {
	w.status = status
	if w.onHeader != nil {
		w.onHeader()
	}
}
func (w *sweepWriter) Write(p []byte) (int, error) { return w.body.Write(p) }

func TestCachedResponseSurvivesConcurrentBodyExpiry(t *testing.T) {
	secret := strings.Repeat("s", 32)
	clock := &fakeClock{t: time.Now().UTC()}
	fake := &fakeNode{report: bytes.Repeat([]byte("x"), 64)}
	h := &Handler{Node: fake, Secret: secret, Now: clock.now}
	id := fixedRequestID()
	body := rpcBody(t, "readReport", map[string]any{"taskId": "t", "relative": "log.txt"})
	send := func(w http.ResponseWriter) {
		now := clock.now()
		r := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewReader(body))
		r.Header.Set(HeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
		r.Header.Set(HeaderRequestID, id)
		r.Header.Set(HeaderSignature, signNodeRequest(secret, now.Unix(), id, body))
		h.ServeHTTP(w, r)
	}
	prime := httptest.NewRecorder()
	send(prime)
	if prime.Code != http.StatusOK || !strings.Contains(prime.Body.String(), "result") {
		t.Fatalf("prime=%d %s", prime.Code, prime.Body.String())
	}
	sw := &sweepWriter{}
	// The body is still valid when the request starts, and expires exactly
	// between the overflow check and the body write.
	sw.onHeader = func() {
		clock.advance(replayBodyTTL + time.Minute)
		if _, _, err := h.replay.begin(fmt.Sprintf("%032x", 12345), clock.now(), time.Hour); err != nil {
			t.Errorf("sweep: %v", err)
		}
	}
	send(sw)
	if sw.status != http.StatusOK {
		t.Fatalf("status=%d", sw.status)
	}
	if sw.body.Len() == 0 {
		t.Fatal("cached duplicate returned an empty body")
	}
	if got := sw.body.String(); !strings.Contains(got, "result") && !strings.Contains(got, codeReplayUnavailable) {
		t.Fatalf("unexpected body %s", got)
	}
}
