package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// Handler exposes one Node over the authenticated remote protocol. It never
// logs request bodies: parameters carry caller-resolved credentials. A request
// ID is registered atomically before dispatch, so concurrent duplicates wait
// for the first result instead of executing the operation again.
type Handler struct {
	Node    Node
	Secret  string
	Now     func() time.Time
	MaxBody int64
	replay  replayCache
}

// Budgets are variables so tests can exercise the limits.
var (
	replayMaxEntries = 8192
	replayMaxBody    = 4 << 20
	replayMaxTotal   = 64 << 20
)

const (
	// replayBodyTTL frees a cached response early to bound memory.
	replayBodyTTL = 2 * signatureSkew
	// replayMarkerMargin covers timestamp skew around the retry deadline.
	replayMarkerMargin = 2 * signatureSkew
)

var errReplayFull = errors.New("replay cache is full")

// replayMarkerTTL keeps the dedup marker at least as long as the caller may
// retry the same request ID. Bodies are released earlier, markers are not.
func replayMarkerTTL(method string) time.Duration {
	return nodeTimeout(method) + replayMarkerMargin
}

type replayEntry struct {
	done          chan struct{}
	body          []byte
	status        int
	completed     bool
	overflow      bool
	bodyExpires   time.Time
	markerExpires time.Time
}

type replayCache struct {
	mu      sync.Mutex
	entries map[string]*replayEntry
	bytes   int
}

// sweep drops expired markers and releases bodies whose shorter TTL passed.
func (c *replayCache) sweep(now time.Time) {
	for id, e := range c.entries {
		if e.completed && !now.Before(e.markerExpires) {
			c.bytes -= len(e.body)
			delete(c.entries, id)
			continue
		}
		if e.completed && e.body != nil && !now.Before(e.bodyExpires) {
			c.bytes -= len(e.body)
			e.body = nil
			e.overflow = true
		}
	}
}

// snapshot returns a consistent view of a completed entry. Taking it under the
// cache lock prevents a concurrent sweep from freeing the body between the
// overflow check and the read.
func (c *replayCache) snapshot(e *replayEntry) (int, []byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return e.status, e.body, e.overflow
}

// begin registers a request ID. The first arrival gets isNew=true and must call
// finish. A duplicate gets the shared entry and isNew=false, then waits on
// entry.done. When the cache is full it returns errReplayFull rather than
// evicting records that are still inside the validity window.
func (c *replayCache) begin(id string, now time.Time, markerTTL time.Duration) (*replayEntry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*replayEntry{}
	}
	c.sweep(now)
	if e, ok := c.entries[id]; ok {
		if !e.completed || now.Before(e.markerExpires) {
			return e, false, nil
		}
		c.bytes -= len(e.body)
		delete(c.entries, id)
	}
	if len(c.entries) >= replayMaxEntries {
		return nil, false, errReplayFull
	}
	e := &replayEntry{done: make(chan struct{}), markerExpires: now.Add(markerTTL)}
	c.entries[id] = e
	return e, true, nil
}

// finish publishes the result and wakes every waiter. The body is retained only
// while it fits the per-entry and total budgets; otherwise the execution marker
// stays so a duplicate is never executed again.
func (c *replayCache) finish(id string, e *replayEntry, status int, body []byte, now time.Time) {
	c.mu.Lock()
	if len(body) <= replayMaxBody && c.bytes+len(body) <= replayMaxTotal {
		e.body = body
		e.bodyExpires = now.Add(replayBodyTTL)
		c.bytes += len(body)
	} else {
		e.overflow = true
	}
	e.status = status
	e.completed = true
	c.mu.Unlock()
	close(e.done)
}

func (h *Handler) clock() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/execute" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Node == nil || h.Secret == "" {
		http.Error(w, "execution node is not configured", http.StatusServiceUnavailable)
		return
	}
	max := h.MaxBody
	if max <= 0 {
		max = 1 << 20
	}
	body, e := io.ReadAll(io.LimitReader(r.Body, max+1))
	if e != nil {
		http.Error(w, "cannot read request", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > max {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	requestID := r.Header.Get(HeaderRequestID)
	if !validRequestID(requestID) {
		http.Error(w, "invalid request id", http.StatusBadRequest)
		return
	}
	now := h.clock()
	if !VerifyNodeRequest(h.Secret, r.Header.Get(HeaderTimestamp), requestID, r.Header.Get(HeaderSignature), body, now) {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	var req rpcRequest
	if e = json.Unmarshal(body, &req); e != nil {
		writeRPC(w, http.StatusBadRequest, nil, errors.New("invalid request"))
		return
	}
	entry, isNew, e := h.replay.begin(requestID, now, replayMarkerTTL(req.Method))
	if e != nil {
		http.Error(w, "execution node replay cache is full", http.StatusServiceUnavailable)
		return
	}
	if !isNew {
		select {
		case <-entry.done:
			// The body can be released by a concurrent sweep after done closes,
			// so take one locked snapshot instead of reading fields separately.
			status, cached, overflow := h.replay.snapshot(entry)
			if overflow || len(cached) == 0 {
				writeRPC(w, status, nil, ErrReplayUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(cached)
			return
		case <-r.Context().Done():
			return
		}
	}
	result, err := h.dispatch(r.Context(), req)
	encoded := encodeRPC(result, err)
	h.replay.finish(requestID, entry, http.StatusOK, encoded, h.clock())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func (h *Handler) dispatch(ctx context.Context, req rpcRequest) (any, error) {
	switch req.Method {
	case "prepareWorkspace":
		var p workspaceParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return h.Node.PrepareWorkspace(ctx, p.TaskID, p.Project, p.Env)
	case "inspectWorkspace":
		var p workspaceParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return h.Node.InspectWorkspace(ctx, p.Workspace, p.Project, p.Env)
	case "refreshTargets":
		var p workspaceParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return h.Node.RefreshTargets(ctx, p.Workspace, p.Project, p.Env)
	case "releaseWorkspace":
		var p workspaceParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return nil, h.Node.ReleaseWorkspace(ctx, p.Workspace)
	case "integrationStart":
		var p integrationParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return nil, h.Node.StartIntegration(ctx, p.Workspace, p.Config, p.Env)
	case "integrationVerify":
		var p integrationParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return h.Node.VerifyIntegration(ctx, p.Workspace, p.Config, p.Env)
	case "integrationStop":
		var p integrationParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return nil, h.Node.StopIntegration(ctx, p.Workspace, p.Config, p.Env)
	case "readReport":
		var p reportParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return h.Node.ReadReport(ctx, p.TaskID, p.Relative)
	case "cleanup":
		var p cleanupParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return h.Node.Cleanup(ctx, p.TaskID, p.Workspace, p.Config, p.Env)
	case "retire":
		var p retireParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return nil, h.Node.Retire(ctx, p.Task)
	case "agentStart":
		var p agentParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return h.Node.Start(ctx, p.Request.request())
	case "agentInspect":
		var p idParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return h.Node.Inspect(ctx, p.ExecutionID)
	case "agentCancel":
		var p idParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return nil, h.Node.Cancel(ctx, p.ExecutionID)
	case "agentCancelTask":
		var p idParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return nil, h.Node.CancelTask(ctx, p.TaskID)
	case "agentTaskExecutions":
		var p idParams
		if e := decodeParams(req.Params, &p); e != nil {
			return nil, e
		}
		return h.Node.TaskExecutions(ctx, p.TaskID)
	default:
		return nil, errors.New("unknown execution method")
	}
}

func decodeParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return errors.New("missing parameters")
	}
	if e := json.Unmarshal(raw, v); e != nil {
		return errors.New("invalid parameters")
	}
	return nil
}

func encodeRPC(result any, err error) []byte {
	resp := rpcResponse{}
	if err != nil {
		resp.Error = err.Error()
		resp.Code = errorCode(err)
	}
	if result != nil {
		if b, e := json.Marshal(result); e == nil {
			resp.Result = b
		}
	}
	body, _ := json.Marshal(resp)
	return body
}

func writeRPC(w http.ResponseWriter, status int, result any, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encodeRPC(result, err))
}
