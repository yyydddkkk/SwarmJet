package forager

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// fakeStore is an in-memory store. Every call the supervisor makes is
// deterministic and observable.
type fakeStore struct {
	mu       sync.Mutex
	now      func() time.Time
	hives    map[string]domain.Hive
	agents   map[string]domain.Agent
	messages []domain.Message
	tokens   map[string]domain.Token // plaintext -> token
	tokenSeq int
	calls    []string
	sent     []domain.Message

	unhandledErr  error
	setSessionErr error
	setPRErr      error
	markErr       error
	sendErr       error
	tokenErr      error
	revokeErr     error
	transitionErr error

	methodology      *domain.Methodology
	methodologyFiles []domain.MethodologyFile
	methodologyErr   error
}

func newFakeStore(now func() time.Time) *fakeStore {
	return &fakeStore{
		now:    now,
		hives:  make(map[string]domain.Hive),
		agents: make(map[string]domain.Agent),
		tokens: make(map[string]domain.Token),
	}
}

func (s *fakeStore) addHive(h domain.Hive) domain.Hive {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h.CreatedAt.IsZero() {
		h.CreatedAt = s.now()
	}
	h.UpdatedAt = h.CreatedAt
	s.hives[h.ID] = h
	return h
}

func (s *fakeStore) addAgent(a domain.Agent) domain.Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = s.now()
	}
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = a.CreatedAt
	}
	if a.Role == "" {
		a.Role = domain.RoleForager
	}
	if a.State == "" {
		a.State = domain.AgentCreating
	}
	s.agents[a.ID] = a
	return a
}

func (s *fakeStore) addMessage(m domain.Message) domain.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		m.ID = fmt.Sprintf("msg-%d", len(s.messages)+1)
	}
	if m.State == "" {
		m.State = domain.MessagePending
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = s.now()
	}
	s.messages = append(s.messages, m)
	return m
}

func (s *fakeStore) Agent(_ context.Context, id string) (domain.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agents[id]
	if !ok {
		return domain.Agent{}, domain.ErrNotFound
	}
	return a, nil
}

func (s *fakeStore) Hive(_ context.Context, id string) (domain.Hive, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hives[id]
	if !ok {
		return domain.Hive{}, domain.ErrNotFound
	}
	return h, nil
}

func (s *fakeStore) ActiveMethodology(_ context.Context, _ string) (domain.Methodology, []domain.MethodologyFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.methodologyErr != nil {
		return domain.Methodology{}, nil, s.methodologyErr
	}
	if s.methodology == nil {
		return domain.Methodology{}, nil, domain.ErrNotFound
	}
	return *s.methodology, append([]domain.MethodologyFile(nil), s.methodologyFiles...), nil
}

func (s *fakeStore) Hives(context.Context) ([]domain.Hive, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Hive, 0, len(s.hives))
	for _, h := range s.hives {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *fakeStore) Foragers(_ context.Context, hiveID string, states ...domain.AgentState) ([]domain.Agent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	allowed := make(map[domain.AgentState]bool, len(states))
	for _, state := range states {
		allowed[state] = true
	}
	out := make([]domain.Agent, 0)
	for _, a := range s.agents {
		if a.HiveID != hiveID || a.Role != domain.RoleForager {
			continue
		}
		if len(allowed) > 0 && !allowed[a.State] {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *fakeStore) TransitionAgent(_ context.Context, id string, from, to domain.AgentState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transitionErr != nil {
		return s.transitionErr
	}
	a, ok := s.agents[id]
	if !ok {
		return domain.ErrNotFound
	}
	if a.State != from {
		return domain.ErrConflict
	}
	a.State = to
	a.UpdatedAt = s.now()
	s.agents[id] = a
	return nil
}

func (s *fakeStore) SetAgentSession(_ context.Context, id, sessionRef string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "SetAgentSession:"+id+":"+sessionRef)
	if s.setSessionErr != nil {
		return s.setSessionErr
	}
	a, ok := s.agents[id]
	if !ok {
		return domain.ErrNotFound
	}
	a.SessionRef = sessionRef
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = s.now()
	}
	s.agents[id] = a
	return nil
}

func (s *fakeStore) SetAgentPR(_ context.Context, id string, pr int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, fmt.Sprintf("SetAgentPR:%s:%d", id, pr))
	if s.setPRErr != nil {
		return s.setPRErr
	}
	a, ok := s.agents[id]
	if !ok {
		return domain.ErrNotFound
	}
	a.PRNumber = pr
	s.agents[id] = a
	return nil
}

func (s *fakeStore) Unhandled(_ context.Context, hiveID string, to domain.Address, limit int) ([]domain.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unhandledErr != nil {
		return nil, s.unhandledErr
	}
	out := make([]domain.Message, 0)
	for _, m := range s.messages {
		if m.HiveID != hiveID || m.To != to {
			continue
		}
		if m.State != domain.MessagePending && m.State != domain.MessageDelivered {
			continue
		}
		out = append(out, m)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *fakeStore) MarkMessageHandled(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "MarkMessageHandled:"+id)
	if s.markErr != nil {
		return s.markErr
	}
	for i := range s.messages {
		if s.messages[i].ID == id {
			s.messages[i].State = domain.MessageHandled
			return nil
		}
	}
	return domain.ErrNotFound
}

func (s *fakeStore) CreateAgentToken(_ context.Context, agentID string) (string, domain.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "CreateAgentToken:"+agentID)
	if s.tokenErr != nil {
		return "", domain.Token{}, s.tokenErr
	}
	s.tokenSeq++
	plaintext := fmt.Sprintf("agent-token-%d", s.tokenSeq)
	token := domain.Token{
		ID:        fmt.Sprintf("token-%d", s.tokenSeq),
		Name:      "forager",
		Scope:     domain.TokenAgent,
		AgentID:   agentID,
		CreatedAt: s.now(),
	}
	s.tokens[plaintext] = token
	return plaintext, token, nil
}

func (s *fakeStore) RevokeAgentTokens(_ context.Context, agentID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "RevokeAgentTokens:"+agentID)
	if s.revokeErr != nil {
		return s.revokeErr
	}
	for plaintext, token := range s.tokens {
		if token.AgentID == agentID {
			delete(s.tokens, plaintext)
		}
	}
	return nil
}

func (s *fakeStore) SendMessage(_ context.Context, m domain.Message) (domain.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return domain.Message{}, s.sendErr
	}
	if m.ID == "" {
		m.ID = fmt.Sprintf("sent-%d", len(s.sent)+1)
	}
	if m.State == "" {
		m.State = domain.MessagePending
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = s.now()
	}
	s.sent = append(s.sent, m)
	s.messages = append(s.messages, m)
	return m, nil
}

// --- test helpers ---------------------------------------------------------

func (s *fakeStore) state(id string) domain.AgentState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agents[id].State
}

func (s *fakeStore) agentCopy(id string) domain.Agent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agents[id]
}

func (s *fakeStore) setAgent(a domain.Agent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.agents[a.ID] = a
}

func (s *fakeStore) isHandled(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.messages {
		if m.ID == id {
			return m.State == domain.MessageHandled
		}
	}
	return false
}

func (s *fakeStore) callCount(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, call := range s.calls {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}

func (s *fakeStore) reportsFrom(agentID string) []domain.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.Message, 0)
	for _, m := range s.sent {
		if m.Kind == domain.MessageReport && m.From == domain.ForagerAddress(agentID) {
			out = append(out, m)
		}
	}
	return out
}

func (s *fakeStore) tokenCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tokens)
}

func (s *fakeStore) setHiveConfig(id string, mutate func(*domain.HiveConfig)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hives[id]
	mutate(&h.Config)
	s.hives[id] = h
}

// fakeClock advances only when the test says so.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at    time.Time
	ch    chan time.Time
	fired bool
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.timers = append(c.timers, &fakeTimer{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	due := make([]*fakeTimer, 0)
	remaining := c.timers[:0]
	for _, timer := range c.timers {
		if !timer.at.After(c.now) {
			timer.fired = true
			due = append(due, timer)
			continue
		}
		remaining = append(remaining, timer)
	}
	c.timers = remaining
	now := c.now
	c.mu.Unlock()
	for _, timer := range due {
		select {
		case timer.ch <- now:
		default:
		}
	}
}

func (c *fakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, timer := range c.timers {
		if !timer.fired {
			n++
		}
	}
	return n
}

// fakeRuntime scripts one Cell per Agent.
type fakeRuntime struct {
	mu       sync.Mutex
	scripts  map[string]*runtimeScript
	sessions map[string]*fakeSession
	starts   []cell.Spec
	stops    []string
	removes  []string
	destroys []string
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		scripts:  make(map[string]*runtimeScript),
		sessions: make(map[string]*fakeSession),
	}
}

func (r *fakeRuntime) script(agentID string) *runtimeScript {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.scripts[agentID]; ok {
		return s
	}
	s := &runtimeScript{release: make(chan struct{})}
	r.scripts[agentID] = s
	return s
}

func (r *fakeRuntime) Start(ctx context.Context, spec cell.Spec) (cell.Session, error) {
	script := r.script(spec.AgentID)
	r.mu.Lock()
	r.starts = append(r.starts, spec)
	r.mu.Unlock()

	script.mu.Lock()
	if len(script.startErrs) > 0 {
		err := script.startErrs[0]
		script.startErrs = script.startErrs[1:]
		script.mu.Unlock()
		return nil, err
	}
	block := script.startBlock
	ref := spec.SessionRef
	if ref == "" {
		ref = script.ref
	}
	if ref == "" {
		ref = "session-" + spec.AgentID
	}
	script.mu.Unlock()

	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	session := &fakeSession{
		script:  script,
		agentID: spec.AgentID,
		ref:     ref,
		closeCh: make(chan struct{}),
	}
	r.mu.Lock()
	r.sessions[spec.AgentID] = session
	r.mu.Unlock()
	return session, nil
}

func (r *fakeRuntime) Stop(_ context.Context, agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stops = append(r.stops, agentID)
	return nil
}

func (r *fakeRuntime) Remove(_ context.Context, agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removes = append(r.removes, agentID)
	return nil
}

func (r *fakeRuntime) Destroy(_ context.Context, agentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.destroys = append(r.destroys, agentID)
	return nil
}

func (r *fakeRuntime) startsFor(agentID string) []cell.Spec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]cell.Spec, 0)
	for _, spec := range r.starts {
		if spec.AgentID == agentID {
			out = append(out, spec)
		}
	}
	return out
}

func (r *fakeRuntime) count(values []string, agentID string) int {
	n := 0
	for _, value := range values {
		if value == agentID {
			n++
		}
	}
	return n
}

func (r *fakeRuntime) stopCount(agentID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count(r.stops, agentID)
}

func (r *fakeRuntime) removeCount(agentID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count(r.removes, agentID)
}

func (r *fakeRuntime) destroyCount(agentID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count(r.destroys, agentID)
}

func (r *fakeRuntime) sessionFor(agentID string) *fakeSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[agentID]
}

type runtimeScript struct {
	mu           sync.Mutex
	startErrs    []error
	promptErrs   []error
	startBlock   chan struct{}
	blockPrompts int
	released     bool
	release      chan struct{}
	ref          string
	updates      []cell.Update
	promptTexts  []string
	promptCount  int
	cancels      int
}

func (s *runtimeScript) releasePrompt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.released {
		close(s.release)
		s.released = true
	}
}

func (s *runtimeScript) prompts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.promptTexts...)
}

func (s *runtimeScript) promptTotal() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.promptCount
}

func (s *runtimeScript) cancelTotal() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancels
}

type fakeSession struct {
	script  *runtimeScript
	agentID string
	ref     string

	mu      sync.Mutex
	closed  bool
	closeCh chan struct{}
}

func (s *fakeSession) Ref() string { return s.ref }

func (s *fakeSession) Prompt(ctx context.Context, text string, sink func(cell.Update)) (cell.TurnResult, error) {
	s.script.mu.Lock()
	s.script.promptCount++
	s.script.promptTexts = append(s.script.promptTexts, text)
	block := s.script.blockPrompts > 0
	if block {
		s.script.blockPrompts--
	}
	var promptErr error
	if len(s.script.promptErrs) > 0 {
		promptErr = s.script.promptErrs[0]
		s.script.promptErrs = s.script.promptErrs[1:]
	}
	release := s.script.release
	updates := append([]cell.Update(nil), s.script.updates...)
	s.script.mu.Unlock()

	for _, update := range updates {
		sink(update)
	}
	if block {
		select {
		case <-ctx.Done():
			return cell.TurnResult{StopReason: "cancelled"}, ctx.Err()
		case <-s.closeCh:
			return cell.TurnResult{StopReason: "cancelled"}, nil
		case <-release:
			return cell.TurnResult{StopReason: "end_turn"}, nil
		}
	}
	if promptErr != nil {
		return cell.TurnResult{}, promptErr
	}
	return cell.TurnResult{StopReason: "end_turn"}, nil
}

func (s *fakeSession) Cancel(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	close(s.closeCh)
	s.closed = true
	s.script.mu.Lock()
	s.script.cancels++
	s.script.mu.Unlock()
	return nil
}

func (s *fakeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		close(s.closeCh)
		s.closed = true
	}
	return nil
}

type publishCall struct {
	spec  cell.Spec
	title string
	body  string
}

type fakePublisher struct {
	mu    sync.Mutex
	calls []publishCall
	pr    int64
	err   error
}

func (p *fakePublisher) Publish(_ context.Context, spec cell.Spec, title, body string) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, publishCall{spec: spec, title: title, body: body})
	return p.pr, p.err
}

func (p *fakePublisher) callsCopy() []publishCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]publishCall(nil), p.calls...)
}

func eventually(t *testing.T, timeout time.Duration, cond func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out: %s", message)
}
