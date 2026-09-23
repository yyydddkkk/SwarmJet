package execution

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarmjet/internal/domain"
	"swarmjet/internal/workspace"
)

// Wire headers for the authenticated control-plane to node protocol.
const (
	HeaderTimestamp = "X-SwarmJet-Timestamp"
	HeaderSignature = "X-SwarmJet-Signature"
	HeaderRequestID = "X-SwarmJet-Request-ID"
	signatureSkew   = 2 * time.Minute
	maxNodeResponse = 32 << 20
)

// Structured error codes let the caller rebuild typed errors that the pipeline
// depends on, such as workspace.ErrNeedsSync which triggers a rework instead of
// a blocked task.
const (
	codeNeedsSync         = "needs_sync"
	codeReplayUnavailable = "replay_unavailable"
)

// ErrReplayUnavailable reports that a duplicate request's original response was
// too large to retain. The operation already ran and must not run again.
var ErrReplayUnavailable = errors.New("duplicate request; the original response was not retained")

var requestIDPattern = regexp.MustCompile(`^[a-f0-9]{16,64}$`)

func newNodeRequestID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}

func validRequestID(id string) bool { return requestIDPattern.MatchString(id) }

func errorCode(err error) string {
	switch {
	case errors.Is(err, workspace.ErrNeedsSync):
		return codeNeedsSync
	case errors.Is(err, ErrReplayUnavailable):
		return codeReplayUnavailable
	default:
		return ""
	}
}

// codedError rebuilds the typed error a structured code represents.
func codedError(code, message string) error {
	switch code {
	case codeNeedsSync:
		return fmt.Errorf("%w: %s", workspace.ErrNeedsSync, message)
	case codeReplayUnavailable:
		return ErrReplayUnavailable
	default:
		return errors.New(message)
	}
}

// signNodeRequest binds the timestamp, request ID and body. Including the
// request ID keeps it inside the signature so a replay cannot change it.
func signNodeRequest(secret string, unix int64, requestID string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(unix, 10)))
	mac.Write([]byte{10})
	mac.Write([]byte(requestID))
	mac.Write([]byte{10})
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyNodeRequest checks the timestamp window and HMAC of a node request.
// Empty secrets or request IDs never authenticate.
func VerifyNodeRequest(secret, timestamp, requestID, signature string, body []byte, now time.Time) bool {
	if secret == "" || timestamp == "" || requestID == "" || signature == "" {
		return false
	}
	unix, e := strconv.ParseInt(timestamp, 10, 64)
	if e != nil {
		return false
	}
	at := time.Unix(unix, 0)
	if now.Sub(at) > signatureSkew || at.Sub(now) > signatureSkew {
		return false
	}
	return hmac.Equal([]byte(signNodeRequest(secret, unix, requestID, body)), []byte(signature))
}

// nodeTimeouts bounds every remote call so a stalled node releases the Activity
// and its concurrency slot instead of waiting for the two-hour activity limit.
var nodeTimeouts = map[string]time.Duration{
	"agentStart":          2 * time.Minute,
	"agentInspect":        2 * time.Minute,
	"agentCancel":         2 * time.Minute,
	"agentCancelTask":     2 * time.Minute,
	"agentTaskExecutions": 2 * time.Minute,
	"readReport":          2 * time.Minute,
	"syncManagedMeta":     10 * time.Minute,
	"releaseWorkspace":    5 * time.Minute,
	"prepareWorkspace":    30 * time.Minute,
	"refreshTargets":      30 * time.Minute,
	"cleanup":             30 * time.Minute,
	"retire":              30 * time.Minute,
	"integrationStart":    30 * time.Minute,
	"integrationVerify":   30 * time.Minute,
	"integrationStop":     30 * time.Minute,
}

func nodeTimeout(method string) time.Duration {
	if d, ok := nodeTimeouts[method]; ok {
		return d
	}
	return 10 * time.Minute
}

// Transport retries reuse one request ID so a lost response is not executed
// twice. A new call (poll or new operation) generates a new ID.
const (
	nodeMaxAttempts  = 4
	nodeRetryBackoff = 100 * time.Millisecond
)

// nodeHTTPClient clones the client with redirects disabled. A 307/308 would
// otherwise resend the credential-bearing body to whatever address the node or
// a proxy names. Per-call context deadlines bound the wait.
func nodeHTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	c := *base
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &c
}

type rpcRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Code   string          `json:"code,omitempty"`
}

type workspaceParams struct {
	TaskID    string            `json:"taskId,omitempty"`
	Project   domain.Project    `json:"project"`
	Workspace domain.Workspace  `json:"workspace,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

type syncManagedMetaParams struct {
	ProjectID string `json:"projectId"`
	Ref       string `json:"ref"`
	Bundle    []byte `json:"bundle"`
}

type idParams struct {
	ExecutionID string `json:"executionId,omitempty"`
	TaskID      string `json:"taskId,omitempty"`
}

// agentWire is the transport form of AgentRequest. AgentRequest itself keeps
// Env out of JSON so credentials never reach Temporal; the remote protocol adds
// it back for the authenticated node call only.
type agentWire struct {
	ExecutionID string             `json:"executionId"`
	TaskID      string             `json:"taskId"`
	Role        string             `json:"role"`
	Workspace   string             `json:"workspace"`
	Prompt      string             `json:"prompt"`
	SessionID   string             `json:"sessionId"`
	Config      domain.AgentConfig `json:"config"`
	Env         map[string]string  `json:"env,omitempty"`
}

func wireAgent(req domain.AgentRequest) agentWire {
	return agentWire{ExecutionID: req.ExecutionID, TaskID: req.TaskID, Role: req.Role, Workspace: req.Workspace, Prompt: req.Prompt, SessionID: req.SessionID, Config: req.Config, Env: req.Env}
}
func (w agentWire) request() domain.AgentRequest {
	return domain.AgentRequest{ExecutionID: w.ExecutionID, TaskID: w.TaskID, Role: w.Role, Workspace: w.Workspace, Prompt: w.Prompt, SessionID: w.SessionID, Config: w.Config, Env: w.Env}
}

type agentParams struct {
	Request agentWire `json:"request"`
}
type integrationParams struct {
	Workspace domain.Workspace         `json:"workspace"`
	Config    domain.IntegrationConfig `json:"config"`
	Env       map[string]string        `json:"env,omitempty"`
}
type reportParams struct {
	TaskID   string `json:"taskId"`
	Relative string `json:"relative"`
}
type cleanupParams struct {
	TaskID    string                   `json:"taskId"`
	Workspace domain.Workspace         `json:"workspace"`
	Config    domain.IntegrationConfig `json:"config"`
	Env       map[string]string        `json:"env,omitempty"`
}
type retireParams struct {
	Task domain.Task `json:"task"`
}

// RemoteNode talks to an execution node over the authenticated HTTP protocol.
type RemoteNode struct {
	NodeID  string
	BaseURL string
	Secret  string
	HTTP    *http.Client
	// Now is injectable so tests can advance both client and server clocks.
	Now func() time.Time
}

func NewRemoteNode(id, baseURL, secret string, client *http.Client) *RemoteNode {
	return &RemoteNode{NodeID: id, BaseURL: strings.TrimRight(baseURL, "/"), Secret: secret, HTTP: nodeHTTPClient(client)}
}
func (n *RemoteNode) ID() string { return n.NodeID }
func (n *RemoteNode) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

func (n *RemoteNode) call(ctx context.Context, method string, params, out any) error {
	var raw json.RawMessage
	if params != nil {
		b, e := json.Marshal(params)
		if e != nil {
			return e
		}
		raw = b
	}
	body, e := json.Marshal(rpcRequest{Method: method, Params: raw})
	if e != nil {
		return e
	}
	requestID, e := newNodeRequestID()
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, nodeTimeout(method))
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < nodeMaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return lastErr
			case <-time.After(nodeRetryBackoff * time.Duration(attempt)):
			}
		}
		retryable, err := n.attempt(ctx, requestID, body, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable || ctx.Err() != nil {
			return err
		}
	}
	return lastErr
}

// attempt performs one HTTP exchange with the caller's request ID. It reports
// whether the failure can be retried with the same identity.
func (n *RemoteNode) attempt(ctx context.Context, requestID string, body []byte, out any) (bool, error) {
	unix := n.now().UTC().Unix()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, n.BaseURL+"/execute", bytes.NewReader(body))
	if e != nil {
		return false, e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderTimestamp, strconv.FormatInt(unix, 10))
	req.Header.Set(HeaderRequestID, requestID)
	req.Header.Set(HeaderSignature, signNodeRequest(n.Secret, unix, requestID, body))
	resp, e := n.HTTP.Do(req)
	if e != nil {
		return true, fmt.Errorf("execution node %s unavailable: %w", n.NodeID, e)
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, maxNodeResponse))
	if e != nil {
		return true, e
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return false, fmt.Errorf("execution node %s refused redirect", n.NodeID)
	}
	if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode >= 500 {
		return true, fmt.Errorf("execution node %s returned HTTP %d", n.NodeID, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("execution node %s returned HTTP %d", n.NodeID, resp.StatusCode)
	}
	var r rpcResponse
	if e = json.Unmarshal(data, &r); e != nil {
		return false, fmt.Errorf("execution node %s returned invalid response: %w", n.NodeID, e)
	}
	if r.Error != "" {
		return false, codedError(r.Code, r.Error)
	}
	if out != nil && len(r.Result) > 0 {
		if e = json.Unmarshal(r.Result, out); e != nil {
			return false, e
		}
	}
	return false, nil
}

func (n *RemoteNode) SyncManagedMeta(ctx context.Context, projectID, ref string, bundle []byte) error {
	return n.call(ctx, "syncManagedMeta", syncManagedMetaParams{ProjectID: projectID, Ref: ref, Bundle: bundle}, nil)
}
func (n *RemoteNode) PrepareWorkspace(ctx context.Context, taskID string, p domain.Project, env map[string]string) (domain.Workspace, error) {
	var out domain.Workspace
	e := n.call(ctx, "prepareWorkspace", workspaceParams{TaskID: taskID, Project: p, Env: env}, &out)
	return out, e
}
func (n *RemoteNode) InspectWorkspace(ctx context.Context, ws domain.Workspace, p domain.Project, env map[string]string) (domain.Workspace, error) {
	var out domain.Workspace
	e := n.call(ctx, "inspectWorkspace", workspaceParams{Project: p, Workspace: ws, Env: env}, &out)
	return out, e
}
func (n *RemoteNode) RefreshTargets(ctx context.Context, ws domain.Workspace, p domain.Project, env map[string]string) (domain.Workspace, error) {
	var out domain.Workspace
	e := n.call(ctx, "refreshTargets", workspaceParams{Project: p, Workspace: ws, Env: env}, &out)
	return out, e
}
func (n *RemoteNode) ReleaseWorkspace(ctx context.Context, ws domain.Workspace) error {
	return n.call(ctx, "releaseWorkspace", workspaceParams{Workspace: ws}, nil)
}
func (n *RemoteNode) StartIntegration(ctx context.Context, ws domain.Workspace, cfg domain.IntegrationConfig, env map[string]string) error {
	return n.call(ctx, "integrationStart", integrationParams{Workspace: ws, Config: cfg, Env: env}, nil)
}
func (n *RemoteNode) VerifyIntegration(ctx context.Context, ws domain.Workspace, cfg domain.IntegrationConfig, env map[string]string) ([]domain.Verification, error) {
	out := []domain.Verification{}
	e := n.call(ctx, "integrationVerify", integrationParams{Workspace: ws, Config: cfg, Env: env}, &out)
	return out, e
}
func (n *RemoteNode) StopIntegration(ctx context.Context, ws domain.Workspace, cfg domain.IntegrationConfig, env map[string]string) error {
	return n.call(ctx, "integrationStop", integrationParams{Workspace: ws, Config: cfg, Env: env}, nil)
}
func (n *RemoteNode) ReadReport(ctx context.Context, taskID, relative string) ([]byte, error) {
	var out []byte
	e := n.call(ctx, "readReport", reportParams{TaskID: taskID, Relative: relative}, &out)
	return out, e
}
func (n *RemoteNode) Cleanup(ctx context.Context, taskID string, ws domain.Workspace, cfg domain.IntegrationConfig, env map[string]string) (bool, error) {
	var done bool
	e := n.call(ctx, "cleanup", cleanupParams{TaskID: taskID, Workspace: ws, Config: cfg, Env: env}, &done)
	return done, e
}
func (n *RemoteNode) Retire(ctx context.Context, t domain.Task) error {
	return n.call(ctx, "retire", retireParams{Task: t}, nil)
}
func (n *RemoteNode) Start(ctx context.Context, req domain.AgentRequest) (domain.Execution, error) {
	var out domain.Execution
	e := n.call(ctx, "agentStart", agentParams{Request: wireAgent(req)}, &out)
	return out, e
}
func (n *RemoteNode) Inspect(ctx context.Context, id string) (domain.Execution, error) {
	var out domain.Execution
	e := n.call(ctx, "agentInspect", idParams{ExecutionID: id}, &out)
	return out, e
}
func (n *RemoteNode) Cancel(ctx context.Context, id string) error {
	return n.call(ctx, "agentCancel", idParams{ExecutionID: id}, nil)
}
func (n *RemoteNode) CancelTask(ctx context.Context, taskID string) error {
	return n.call(ctx, "agentCancelTask", idParams{TaskID: taskID}, nil)
}
func (n *RemoteNode) TaskExecutions(ctx context.Context, taskID string) ([]domain.Execution, error) {
	out := []domain.Execution{}
	e := n.call(ctx, "agentTaskExecutions", idParams{TaskID: taskID}, &out)
	return out, e
}

var _ Node = (*RemoteNode)(nil)

// AddressLookup resolves a node ID to its registered base URL.
type AddressLookup interface {
	NodeAddress(context.Context, string) (string, error)
}

// Pool builds RemoteNode transports and caches one per node, rebuilding it when
// the registered address changes.
type Pool struct {
	SecretFor func(context.Context, string) (string, error)
	Lookup    AddressLookup
	Secret    string
	HTTP      *http.Client
	// Now is propagated to RemoteNode for deterministic clock tests.
	Now   func() time.Time
	mu    sync.Mutex
	nodes map[string]*RemoteNode
}

func (p *Pool) Node(ctx context.Context, id string) (Node, error) {
	if p == nil || p.Lookup == nil {
		return nil, errors.New("execution node address lookup is not configured")
	}
	address, e := p.Lookup.NodeAddress(ctx, id)
	if e != nil {
		return nil, fmt.Errorf("execution node %s is not registered: %w", id, e)
	}
	if address == "" {
		return nil, fmt.Errorf("execution node %s has no address", id)
	}
	secret := p.Secret
	if p.SecretFor != nil {
		secret, e = p.SecretFor(ctx, id)
		if e != nil {
			return nil, e
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.nodes == nil {
		p.nodes = map[string]*RemoteNode{}
	}
	if existing, ok := p.nodes[id]; ok && existing.BaseURL == strings.TrimRight(address, "/") && existing.Secret == secret {
		return existing, nil
	}
	n := NewRemoteNode(id, address, secret, p.HTTP)
	n.Now = p.Now
	p.nodes[id] = n
	return n, nil
}

var _ NodeResolver = (*Pool)(nil)
