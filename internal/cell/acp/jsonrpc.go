package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// rpcMessage is the union of the three JSON-RPC 2.0 message shapes. ACP uses
// newline-delimited JSON on stdio: every message is one line of compact JSON.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 && string(e.Data) != "null" {
		return fmt.Sprintf("acp: json-rpc error %d: %s (%s)", e.Code, e.Message, strings.TrimSpace(string(e.Data)))
	}
	return fmt.Sprintf("acp: json-rpc error %d: %s", e.Code, e.Message)
}

// rpcConn multiplexes JSON-RPC requests, responses and notifications over one
// bidirectional stream. It is safe for concurrent callers.
type rpcConn struct {
	rw      io.ReadWriteCloser
	br      *bufio.Reader
	writeMu sync.Mutex

	mu        sync.Mutex
	pending   map[string]chan *rpcResponse
	nextID    int64
	closed    chan struct{}
	closeOnce sync.Once
	err       error

	// onRequest handles a request initiated by the agent. It runs on its own
	// goroutine so a slow permission decision cannot stall the read loop.
	onRequest func(method string, params json.RawMessage) (any, *rpcError)
	// onNotify handles an agent notification. It runs on the read loop and must
	// not block for long.
	onNotify func(method string, params json.RawMessage)
}

type rpcResponse struct {
	Result json.RawMessage
	Error  *rpcError
}

func newRPCConn(rw io.ReadWriteCloser) *rpcConn {
	c := &rpcConn{
		rw:      rw,
		br:      bufio.NewReaderSize(rw, 1<<20),
		pending: map[string]chan *rpcResponse{},
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *rpcConn) readLoop() {
	for {
		line, err := c.br.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			c.handle(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.fail(io.EOF)
			} else {
				c.fail(err)
			}
			return
		}
	}
}

func (c *rpcConn) handle(raw []byte) {
	var msg rpcMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		// ACP has no framing recovery; a malformed line is ignored so the
		// spike can observe the stream around it.
		return
	}
	switch {
	case msg.Method != "" && len(msg.ID) > 0:
		go c.handleRequest(msg)
	case msg.Method != "":
		if c.onNotify != nil {
			c.onNotify(msg.Method, msg.Params)
		}
	case len(msg.ID) > 0:
		key := rpcIDKey(msg.ID)
		c.mu.Lock()
		ch := c.pending[key]
		delete(c.pending, key)
		c.mu.Unlock()
		if ch != nil {
			ch <- &rpcResponse{Result: msg.Result, Error: msg.Error}
		}
	}
}

func (c *rpcConn) handleRequest(msg rpcMessage) {
	var (
		result any
		rpcErr *rpcError
	)
	if c.onRequest == nil {
		rpcErr = &rpcError{Code: CodeMethodNotFound, Message: "method not found: " + msg.Method}
	} else {
		result, rpcErr = c.onRequest(msg.Method, msg.Params)
	}
	envelope := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID)}
	if rpcErr != nil {
		envelope["error"] = rpcErr
	} else {
		envelope["result"] = result
	}
	// Best effort: a closed stream fails every pending call anyway.
	_ = c.writeJSON(envelope)
}

// call sends a request and waits for its response or context cancellation.
func (c *rpcConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	id := atomic.AddInt64(&c.nextID, 1)
	key := strconv.FormatInt(id, 10)
	ch := make(chan *rpcResponse, 1)

	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	c.pending[key] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
	}()

	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(key),
		"method":  method,
		"params":  raw,
	}
	if err := c.writeJSON(request); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		c.mu.Lock()
		err := c.err
		c.mu.Unlock()
		if err == nil {
			err = io.ErrClosedPipe
		}
		return nil, err
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// notify sends a notification (a request without an id).
func (c *rpcConn) notify(method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	message := map[string]any{"jsonrpc": "2.0", "method": method}
	if raw != nil {
		message["params"] = raw
	}
	return c.writeJSON(message)
}

func (c *rpcConn) writeJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.rw.Write(payload)
	return err
}

func (c *rpcConn) fail(err error) {
	if err == nil {
		err = io.ErrUnexpectedEOF
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
	c.closeOnce.Do(func() { close(c.closed) })
}

// Close shuts the stream down and fails pending calls.
func (c *rpcConn) Close() error {
	err := c.rw.Close()
	c.fail(io.ErrClosedPipe)
	return err
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		return raw, nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// rpcIDKey canonicalizes a JSON-RPC id so numeric and string ids both match.
func rpcIDKey(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if len(s) >= 2 && s[0] == '"' {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			return v
		}
	}
	return s
}
