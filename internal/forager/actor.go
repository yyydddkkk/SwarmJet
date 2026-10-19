package forager

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// errTerminal means a Forager reached a terminal state while a turn was in
// flight, so the turn must not park it or publish more work.
var errTerminal = errors.New("forager: agent is already terminal")

type promptOutcome int

const (
	promptCompleted promptOutcome = iota
	promptCancelled
	promptTimedOut
	promptAborted
)

type promptResult struct {
	res cell.TurnResult
	err error
}

// actor serializes turns for one Forager. At most one turn runs at a time;
// messages that arrive meanwhile wait in queue and become the next turn.
type actor struct {
	sup    *Supervisor
	id     string
	hiveID string

	mu            sync.Mutex
	queue         []domain.Message
	running       bool
	terminal      bool
	cancelPending bool
	failures      int
	mcpToken      string
	session       cell.Session
	lastTurnEnd   time.Time
	stopped       bool
	interrupt     chan struct{}
}

func newActor(s *Supervisor, id, hiveID string) *actor {
	return &actor{
		sup:       s,
		id:        id,
		hiveID:    hiveID,
		interrupt: make(chan struct{}, 1),
	}
}

// enqueue adds messages to the actor's queue and starts a turn if one is not
// already running. A control message whose body is cancel interrupts the
// current turn instead of waiting for it.
func (a *actor) enqueue(_ context.Context, msgs ...domain.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	a.mu.Lock()
	if a.terminal {
		a.mu.Unlock()
		return nil
	}
	wasRunning := a.running
	cancel := false
	for _, m := range msgs {
		if isCancelMessage(m) {
			a.cancelPending = true
			cancel = true
		}
		a.queue = append(a.queue, m)
	}
	if !a.running {
		a.running = true
	}
	a.mu.Unlock()

	if cancel && wasRunning {
		select {
		case a.interrupt <- struct{}{}:
		default:
		}
	}
	if !wasRunning {
		go a.loop(a.sup.baseContext())
	}
	return nil
}

func (a *actor) loop(ctx context.Context) {
	for {
		a.mu.Lock()
		if a.terminal || len(a.queue) == 0 {
			a.running = false
			a.mu.Unlock()
			return
		}
		batch := a.queue
		a.queue = nil
		a.mu.Unlock()

		if ctx.Err() != nil {
			a.stopRunning()
			return
		}
		if hasCancelMessage(batch) {
			a.terminate(ctx, domain.AgentCancelled, "cancelled by control message", batch, "")
			a.stopRunning()
			return
		}
		if !a.runTurn(ctx, batch) {
			a.stopRunning()
			return
		}
	}
}

func (a *actor) stopRunning() {
	a.mu.Lock()
	a.running = false
	a.mu.Unlock()
}

// runTurn executes one turn and reports whether the loop should continue with
// queued messages.
func (a *actor) runTurn(ctx context.Context, batch []domain.Message) bool {
	store := a.sup.opts.Store

	agent, err := store.Agent(ctx, a.id)
	if err != nil {
		a.sup.log.Warn("forager: load agent failed", "agent", a.id, "error", err)
		return false
	}
	if agent.Role != domain.RoleForager || isTerminal(agent.State) {
		a.mu.Lock()
		a.terminal = true
		a.queue = nil
		a.mu.Unlock()
		return false
	}
	hive, err := store.Hive(ctx, agent.HiveID)
	if err != nil {
		a.sup.log.Warn("forager: load hive failed", "agent", a.id, "hive", agent.HiveID, "error", err)
		return false
	}
	firstTurn := agent.SessionRef == ""

	// Unhandled is authoritative: a duplicate notification for an already
	// handled message must not start a Cell or replay the prompt.
	consumed, err := a.collectMessages(ctx, agent.HiveID)
	if err != nil {
		a.sup.log.Warn("forager: read unhandled messages failed", "agent", agent.ID, "error", err)
		consumed = dedupeMessages(batch)
	}
	if hasCancelMessage(consumed) {
		a.terminate(ctx, domain.AgentCancelled, "cancelled by control message", consumed, "")
		return false
	}
	if len(consumed) == 0 {
		// A duplicate notification; let the loop drain the queue and exit.
		return true
	}

	session, spec, err := a.start(ctx, agent, hive)
	if err != nil {
		if ctx.Err() != nil {
			return false
		}
		a.terminate(ctx, domain.AgentFailed, fmt.Sprintf("failed to start Cell after %d attempts: %v", a.failureCount(), err), consumed, "")
		return false
	}
	if err := store.SetAgentSession(ctx, agent.ID, session.Ref()); err != nil {
		a.sup.log.Warn("forager: save session failed", "agent", agent.ID, "error", err)
	}
	agent, err = a.transitionTo(ctx, domain.AgentActive)
	if err != nil {
		if errors.Is(err, errTerminal) || ctx.Err() != nil {
			return false
		}
		a.sup.log.Warn("forager: mark active failed", "agent", agent.ID, "error", err)
		return false
	}

	consumed, err = a.collectMessages(ctx, agent.HiveID)
	if err != nil {
		a.sup.log.Warn("forager: read unhandled messages failed", "agent", agent.ID, "error", err)
		consumed = dedupeMessages(batch)
	}
	text := buildPrompt(agent, hive, consumed, firstTurn)
	timeout := time.Duration(hive.Config.TurnTimeoutMinutes) * time.Minute
	if timeout <= 0 {
		timeout = 120 * time.Minute
	}

	var lastText string
	for attempt := 1; ; attempt++ {
		var outcome promptOutcome
		lastText, _, err, outcome = a.prompt(ctx, agent, session, text, timeout)
		if a.isTerminal() {
			return false
		}
		switch outcome {
		case promptCancelled:
			a.terminate(ctx, domain.AgentCancelled, "cancelled by control message", consumed, lastText)
			return false
		case promptTimedOut:
			a.terminate(ctx, domain.AgentFailed, fmt.Sprintf("turn timed out after %s", timeout), consumed, lastText)
			return false
		case promptAborted:
			return false
		}
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return false
		}
		a.sup.log.Warn("forager: prompt failed", "agent", agent.ID, "attempt", attempt, "error", err)
		if !a.recordFailure() {
			a.terminate(ctx, domain.AgentFailed, fmt.Sprintf("prompt failed after %d attempts: %v", a.failureCount(), err), consumed, lastText)
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-a.sup.clock.After(a.sup.opts.RetryBackoff(attempt)):
		}
	}
	a.resetFailures()
	if a.cancelRequested() {
		a.terminate(ctx, domain.AgentCancelled, "cancelled by control message", consumed, lastText)
		return false
	}

	// The turn is done: persist consumption, publish the branch, record the PR
	// and report back before parking.
	a.markHandled(ctx, consumed)
	latest, err := store.Agent(ctx, agent.ID)
	if err != nil {
		latest = agent
	}
	title := publishTitle(latest.Task)
	body := reportBody(latest, lastText)
	pr, publishErr := a.sup.opts.Publisher.Publish(ctx, spec, title, body)
	if publishErr != nil {
		a.sup.log.Warn("forager: publish failed", "agent", agent.ID, "error", publishErr)
	}
	if pr != 0 && pr != latest.PRNumber {
		if err := store.SetAgentPR(ctx, agent.ID, pr); err != nil {
			a.sup.log.Warn("forager: save PR failed", "agent", agent.ID, "error", err)
		}
	}
	a.report(ctx, latest, consumed, reportBodyWithError(latest, lastText, publishErr))
	if _, err := a.transitionTo(ctx, domain.AgentParked); err != nil {
		if !errors.Is(err, errTerminal) {
			a.sup.log.Warn("forager: mark parked failed", "agent", agent.ID, "error", err)
		}
		return false
	}
	a.mu.Lock()
	a.lastTurnEnd = a.sup.clock.Now()
	a.stopped = false
	a.mu.Unlock()
	return true
}

// start creates or resumes the Cell, retrying transient failures with
// exponential backoff up to the configured failure budget.
func (a *actor) start(ctx context.Context, agent domain.Agent, hive domain.Hive) (cell.Session, cell.Spec, error) {
	cli := agent.CLI
	if cli == "" {
		cli = hive.Config.CLI
	}
	if cli == "" {
		cli = DefaultCLI
	}
	var spec cell.Spec
	for attempt := 1; ; attempt++ {
		// Token minting and secret resolution can fail transiently too, so they
		// share the retry budget with Runtime.Start.
		token, err := a.token(ctx, agent.ID)
		var env map[string]string
		if err == nil {
			env, err = a.sup.opts.CLIEnv(ctx, hive)
		}
		if err == nil {
			spec = cell.Spec{
				AgentID:    agent.ID,
				HiveID:     agent.HiveID,
				Repository: hive.Repository,
				Branch:     agent.Branch,
				BaseBranch: "",
				CLI:        cli,
				Model:      a.sup.opts.Model,
				SessionRef: agent.SessionRef,
				Env:        env,
				MCPURL:     a.sup.opts.MCPURL,
				MCPToken:   token,
			}
			var session cell.Session
			session, err = a.sup.opts.Runtime.Start(ctx, spec)
			if err == nil {
				a.mu.Lock()
				a.session = session
				a.mu.Unlock()
				return session, spec, nil
			}
		}
		if ctx.Err() != nil {
			return nil, spec, ctx.Err()
		}
		a.sup.log.Warn("forager: start cell failed", "agent", agent.ID, "attempt", attempt, "error", err)
		if !a.recordFailure() {
			return nil, spec, err
		}
		select {
		case <-ctx.Done():
			return nil, spec, ctx.Err()
		case <-a.sup.clock.After(a.sup.opts.RetryBackoff(attempt)):
		}
	}
}

// token mints one Agent-scoped MCP token per actor lifetime. The plaintext is
// kept in memory only and passed to the Cell through cell.Spec.MCPToken.
func (a *actor) token(ctx context.Context, agentID string) (string, error) {
	a.mu.Lock()
	token := a.mcpToken
	a.mu.Unlock()
	if token != "" {
		return token, nil
	}
	plaintext, _, err := a.sup.opts.Store.CreateAgentToken(ctx, agentID)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.mcpToken = plaintext
	a.mu.Unlock()
	return plaintext, nil
}

func (a *actor) collectMessages(ctx context.Context, hiveID string) ([]domain.Message, error) {
	msgs, err := a.sup.opts.Store.Unhandled(ctx, hiveID, domain.ForagerAddress(a.id), a.sup.opts.UnhandledLimit)
	if err != nil {
		return nil, err
	}
	return dedupeMessages(msgs), nil
}

// prompt runs one turn with a timeout and cancellation channel. It keeps the
// last text update for the report.
func (a *actor) prompt(ctx context.Context, agent domain.Agent, session cell.Session, text string, timeout time.Duration) (string, cell.TurnResult, error, promptOutcome) {
	done := make(chan promptResult, 1)
	var mu sync.Mutex
	var lastText string
	sink := func(update cell.Update) {
		if update.Kind == cell.UpdateText && update.Text != "" {
			mu.Lock()
			lastText = truncateRunes(update.Text, cell.MaxUpdateText)
			mu.Unlock()
		}
		if a.sup.opts.OnUpdate != nil {
			a.sup.opts.OnUpdate(agent, update)
		}
	}
	go func() {
		res, err := session.Prompt(ctx, text, sink)
		done <- promptResult{res: res, err: err}
	}()
	timer := a.sup.clock.After(timeout)
	select {
	case r := <-done:
		mu.Lock()
		out := lastText
		mu.Unlock()
		if a.cancelRequested() {
			return out, r.res, r.err, promptCancelled
		}
		return out, r.res, r.err, promptCompleted
	case <-a.interrupt:
		_ = session.Cancel(context.Background())
		r := a.awaitPrompt(ctx, done)
		return lastOutput(&mu, &lastText), r.res, r.err, promptCancelled
	case <-timer:
		_ = session.Cancel(context.Background())
		r := a.awaitPrompt(ctx, done)
		return lastOutput(&mu, &lastText), r.res, r.err, promptTimedOut
	case <-ctx.Done():
		_ = session.Cancel(context.Background())
		r := a.awaitPrompt(ctx, done)
		return lastOutput(&mu, &lastText), r.res, r.err, promptAborted
	}
}

func lastOutput(mu *sync.Mutex, text *string) string {
	mu.Lock()
	defer mu.Unlock()
	return *text
}

func (a *actor) awaitPrompt(ctx context.Context, done <-chan promptResult) promptResult {
	select {
	case r := <-done:
		return r
	case <-ctx.Done():
		return promptResult{}
	case <-a.sup.clock.After(a.sup.opts.TurnAbortWait):
		return promptResult{}
	}
}

// terminate moves the Forager to a terminal state, reports, and cleans up the
// Cell and its Agent tokens. It is idempotent and never blocks on a running
// prompt: it cancels the session and closes it.
func (a *actor) terminate(ctx context.Context, state domain.AgentState, reason string, consumed []domain.Message, output string) {
	a.mu.Lock()
	if a.terminal {
		a.mu.Unlock()
		return
	}
	a.terminal = true
	queued := a.queue
	a.queue = nil
	session := a.session
	a.session = nil
	a.mcpToken = ""
	a.mu.Unlock()

	cleanup := context.WithoutCancel(ctx)
	all := dedupeMessages(append(append([]domain.Message{}, consumed...), queued...))
	a.markHandled(cleanup, all)

	agent, err := a.sup.opts.Store.Agent(cleanup, a.id)
	if err != nil {
		agent = domain.Agent{ID: a.id, HiveID: a.hiveID}
	}
	a.report(cleanup, agent, all, terminalReport(state, reason, output, agent.Handoff))

	if session != nil {
		_ = session.Cancel(context.Background())
		_ = session.Close()
	}
	if _, err := a.transitionTo(cleanup, state); err != nil && !errors.Is(err, errTerminal) && !errors.Is(err, domain.ErrNotFound) {
		a.sup.log.Warn("forager: terminal transition failed", "agent", a.id, "state", state, "error", err)
	}
	if err := a.sup.opts.Runtime.Destroy(cleanup, a.id); err != nil && !errors.Is(err, cell.ErrNoCell) {
		a.sup.log.Warn("forager: destroy cell failed", "agent", a.id, "error", err)
	}
	if err := a.sup.opts.Store.RevokeAgentTokens(cleanup, a.id); err != nil {
		a.sup.log.Warn("forager: revoke agent tokens failed", "agent", a.id, "error", err)
	}
}

func (a *actor) transitionTo(ctx context.Context, target domain.AgentState) (domain.Agent, error) {
	store := a.sup.opts.Store
	var last domain.Agent
	for i := 0; i < 4; i++ {
		agent, err := store.Agent(ctx, a.id)
		if err != nil {
			return last, err
		}
		last = agent
		if agent.State == target {
			return agent, nil
		}
		if isTerminal(agent.State) {
			return agent, errTerminal
		}
		if err := store.TransitionAgent(ctx, agent.ID, agent.State, target); err != nil {
			if errors.Is(err, domain.ErrConflict) {
				continue
			}
			return agent, err
		}
		agent.State = target
		return agent, nil
	}
	return last, domain.ErrConflict
}

func (a *actor) markHandled(ctx context.Context, msgs []domain.Message) {
	for _, m := range msgs {
		if m.ID == "" {
			continue
		}
		if err := a.sup.opts.Store.MarkMessageHandled(ctx, m.ID); err != nil &&
			!errors.Is(err, domain.ErrNotFound) {
			a.sup.log.Warn("forager: mark message handled failed", "message", m.ID, "error", err)
		}
	}
}

// report sends one kind=report message. local:* and queen senders get the
// report directly; everything else goes to the Hive Inbox.
func (a *actor) report(ctx context.Context, agent domain.Agent, consumed []domain.Message, body string) {
	hiveID := agent.HiveID
	if hiveID == "" {
		hiveID = a.hiveID
	}
	to := domain.AddressHive
	chainID := ""
	hop := 0
	if len(consumed) > 0 {
		first := consumed[0]
		chainID = first.ChainID
		hop = first.Hop + 1
		if first.From == domain.AddressQueen || isLocalAddress(first.From) {
			to = first.From
		}
	}
	msg := domain.Message{
		HiveID:  hiveID,
		From:    domain.ForagerAddress(a.id),
		To:      to,
		Kind:    domain.MessageReport,
		ChainID: chainID,
		Hop:     hop,
		Body:    truncateRunes(body, domain.MaxMessageBody),
	}
	if _, err := a.sup.opts.Store.SendMessage(ctx, msg); err != nil {
		a.sup.log.Warn("forager: send report failed", "agent", a.id, "error", err)
	}
}

// scan applies the Hive's lifecycle limits and picks up messages whose
// notification was missed.
func (a *actor) scan(ctx context.Context, agent domain.Agent, hive domain.Hive) error {
	now := a.sup.clock.Now()
	if isTerminal(agent.State) {
		a.mu.Lock()
		a.terminal = true
		a.mu.Unlock()
		return nil
	}
	if lifetime := time.Duration(hive.Config.ForagerMaxLifetimeHours) * time.Hour; lifetime > 0 &&
		!agent.CreatedAt.IsZero() && now.Sub(agent.CreatedAt) >= lifetime {
		a.terminate(ctx, domain.AgentFailed,
			fmt.Sprintf("forager exceeded its %d-hour lifetime", hive.Config.ForagerMaxLifetimeHours), nil, "")
		return nil
	}

	msgs, err := a.sup.opts.Store.Unhandled(ctx, agent.HiveID, domain.ForagerAddress(agent.ID), a.sup.opts.UnhandledLimit)
	if err != nil {
		return err
	}
	if len(msgs) > 0 {
		if err := a.enqueue(ctx, msgs...); err != nil {
			return err
		}
	}

	a.mu.Lock()
	running := a.running
	stopped := a.stopped
	since := a.lastTurnEnd
	a.mu.Unlock()
	if running {
		return nil
	}
	if since.IsZero() {
		since = agent.UpdatedAt
	}
	if since.IsZero() {
		since = agent.CreatedAt
	}

	if agent.State != domain.AgentParked {
		return nil
	}
	if idle := time.Duration(hive.Config.IdleParkMinutes) * time.Minute; idle > 0 && now.Sub(since) >= idle && !stopped {
		if err := a.sup.opts.Runtime.Stop(ctx, agent.ID); err != nil && !errors.Is(err, cell.ErrNoCell) {
			return err
		}
		a.mu.Lock()
		a.stopped = true
		a.mu.Unlock()
	}
	if hibernate := time.Duration(hive.Config.HibernateHours) * time.Hour; hibernate > 0 && now.Sub(since) >= hibernate {
		if err := a.sup.opts.Runtime.Remove(ctx, agent.ID); err != nil && !errors.Is(err, cell.ErrNoCell) {
			return err
		}
		if _, err := a.transitionTo(ctx, domain.AgentHibernated); err != nil && !errors.Is(err, errTerminal) {
			return err
		}
		a.mu.Lock()
		a.stopped = true
		a.mu.Unlock()
	}
	return nil
}

func (a *actor) recordFailure() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures++
	return a.failures < a.sup.opts.MaxFailures
}

func (a *actor) resetFailures() {
	a.mu.Lock()
	a.failures = 0
	a.mu.Unlock()
}

func (a *actor) failureCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.failures
}

func (a *actor) cancelRequested() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancelPending
}

func (a *actor) isTerminal() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.terminal
}
