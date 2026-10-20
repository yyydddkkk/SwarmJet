package docker

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/cell/acp"
)

// ACPClient is the part of acp.Client the Cell runtime uses. It is an interface
// so tests can drive a scripted agent without a container.
type ACPClient interface {
	Initialize(context.Context) (acp.InitializeResult, error)
	NewSession(context.Context, string, []acp.MCPServer) (acp.NewSessionResult, error)
	LoadSession(context.Context, string, string, []acp.MCPServer, acp.UpdateSink) (acp.LoadSessionResult, error)
	Prompt(context.Context, string, []acp.ContentBlock, acp.UpdateSink) (acp.PromptResult, error)
	Cancel(context.Context, string) error
	Close() error
}

// Dialer starts `omp acp` inside a container and returns a client. Tests
// provide a fake that returns an in-process agent.
type Dialer interface {
	Dial(ctx context.Context, container, workdir string, env, argv []string, permission acp.PermissionHandler) (ACPClient, error)
}

// DockerDialer connects to the CLI with `docker exec -i`, always from the
// workspace so omp attaches its sessions to a stable cwd.
type DockerDialer struct {
	Binary string
}

func (d *DockerDialer) Dial(ctx context.Context, container, workdir string, env, argv []string, permission acp.PermissionHandler) (ACPClient, error) {
	proc, err := acp.StartDockerExecIn(ctx, d.Binary, container, workdir, env, argv)
	if err != nil {
		return nil, err
	}
	return acp.NewClient(proc, acp.WithPermissionHandler(permission)), nil
}

// Session is one connected CLI session. The contract requires one turn at a
// time; concurrent calls queue on turn.
type Session struct {
	client ACPClient
	id     string
	now    func() time.Time

	turn sync.Mutex

	sinkMu sync.Mutex
	sink   func(cell.Update)
	emitMu sync.Mutex

	closeOnce sync.Once
	closeErr  error
}

var _ cell.Session = (*Session)(nil)

// Ref returns the CLI session ID. Callers persist it and pass it back as
// Spec.SessionRef on the next Start.
func (s *Session) Ref() string { return s.id }

func (s *Session) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// Prompt runs one turn and streams updates to sink. The sink is called from the
// ACP update dispatcher, never from the JSON-RPC read loop.
func (s *Session) Prompt(ctx context.Context, text string, sink func(cell.Update)) (cell.TurnResult, error) {
	if text == "" {
		return cell.TurnResult{}, errors.New("cell: prompt is empty")
	}
	s.turn.Lock()
	defer s.turn.Unlock()

	s.setSink(sink)
	defer s.setSink(nil)

	res, err := s.client.Prompt(ctx, s.id, []acp.ContentBlock{acp.TextBlock(text)}, s.onUpdate)
	if err != nil {
		return cell.TurnResult{}, err
	}
	out := cell.TurnResult{StopReason: string(res.StopReason)}
	if res.Usage != nil {
		out.InputTokens = int64(res.Usage.InputTokens)
		out.OutputTokens = int64(res.Usage.OutputTokens)
	}
	return out, nil
}

// Cancel interrupts the current turn. The CLI process and session stay usable.
func (s *Session) Cancel(ctx context.Context) error {
	return s.client.Cancel(ctx, s.id)
}

// Close stops the CLI process. The container keeps running.
func (s *Session) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.client.Close() })
	return s.closeErr
}

func (s *Session) setSink(sink func(cell.Update)) {
	s.sinkMu.Lock()
	s.sink = sink
	s.sinkMu.Unlock()
}

func (s *Session) emit(update cell.Update) {
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	s.sinkMu.Lock()
	sink := s.sink
	s.sinkMu.Unlock()
	if sink != nil {
		sink(update)
	}
}

func (s *Session) onUpdate(n acp.SessionNotification) error {
	u, err := n.Decode()
	if err != nil {
		return nil
	}
	var update cell.Update
	switch u.SessionUpdate {
	case acp.UpdateAgentMessageChunk:
		update = cell.Update{Kind: cell.UpdateText, Text: truncateText(u.Text(), cell.MaxUpdateText)}
	case acp.UpdateAgentThoughtChunk:
		update = cell.Update{Kind: cell.UpdateThought, Text: truncateText(u.Text(), cell.MaxUpdateText)}
	case acp.UpdateToolCall:
		update = cell.Update{Kind: cell.UpdateToolCall, Title: u.Title, Text: truncateText(string(u.RawInput), cell.MaxUpdateText)}
	case acp.UpdateToolCallUpdate:
		kind := cell.UpdateToolCall
		if u.Status == acp.ToolStatusCompleted || u.Status == acp.ToolStatusFailed {
			kind = cell.UpdateToolDone
		}
		update = cell.Update{Kind: kind, Title: u.Title, Text: truncateText(string(u.RawOutput), cell.MaxUpdateText)}
	case acp.UpdatePlan:
		update = cell.Update{Kind: cell.UpdatePlan, Text: truncateText(string(u.Raw), cell.MaxUpdateText)}
	default:
		return nil
	}
	if update.Text == "" && update.Title == "" {
		return nil
	}
	update.At = s.clock()
	s.emit(update)
	return nil
}

// permission answers session/request_permission. A Cell container is the
// isolation boundary, so routine tool use is approved once automatically and
// reported as an update; platform-level Guard checks happen outside the Cell.
func (s *Session) permission(_ context.Context, req acp.PermissionRequest) (acp.PermissionOutcome, error) {
	opt, ok := req.Select(acp.PermissionAllowOnce)
	if !ok {
		return acp.Cancelled(), nil
	}
	title := req.ToolCall.Title
	if title == "" {
		title = req.ToolCall.Kind
	}
	s.emit(cell.Update{
		Kind:  cell.UpdateToolCall,
		Title: "permission: " + title,
		Text:  truncateText(string(req.ToolCall.RawInput), cell.MaxUpdateText),
		At:    s.clock(),
	})
	return acp.Selected(opt), nil
}

// truncateText caps text at max runes without splitting a UTF-8 sequence.
func truncateText(text string, max int) string {
	if max <= 0 || text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max])
}
