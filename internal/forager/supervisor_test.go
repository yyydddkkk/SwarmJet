package forager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

const (
	testHiveID  = "hive-1"
	testAgentID = "agent-1"
	testBranch  = "swarm/hive-1/agent-1"
)

type fixture struct {
	t         *testing.T
	store     *fakeStore
	runtime   *fakeRuntime
	publisher *fakePublisher
	clock     *fakeClock
	sup       *Supervisor
}

func newFixture(t *testing.T, mutate func(*Options)) *fixture {
	t.Helper()
	clock := newFakeClock(time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	store := newFakeStore(clock.Now)
	store.addHive(domain.Hive{
		ID:         testHiveID,
		Name:       "Acme",
		Repository: "acme/widgets",
		Config: domain.HiveConfig{
			CLI:                     "omp",
			MaxConcurrentForagers:   4,
			MaxHops:                 10,
			MaxForagersPerChain:     10,
			IdleParkMinutes:         30,
			HibernateHours:          24,
			TurnTimeoutMinutes:      120,
			ForagerMaxLifetimeHours: 168,
		},
	})
	store.addAgent(domain.Agent{
		ID:      testAgentID,
		HiveID:  testHiveID,
		Role:    domain.RoleForager,
		State:   domain.AgentCreating,
		CLI:     "omp",
		Task:    "Fix the widget\nAdd tests",
		Branch:  testBranch,
		Handoff: &domain.Handoff{Goal: "Fix the widget", Done: "Done so far"},
	})
	opts := Options{
		Store:          store,
		Runtime:        newFakeRuntime(),
		Publisher:      &fakePublisher{pr: 42},
		Clock:          clock,
		Model:          "deepseek/deepseek-chat",
		MCPURL:         "http://mcp.internal/mcp",
		LLMBaseURL:     "http://mcp.internal/llm",
		ScanInterval:   time.Hour,
		RetryBackoff:   func(int) time.Duration { return 0 },
		TurnAbortWait:  time.Second,
		UnhandledLimit: 100,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mutate != nil {
		mutate(&opts)
	}
	runtime, _ := opts.Runtime.(*fakeRuntime)
	publisher, _ := opts.Publisher.(*fakePublisher)
	sup, err := New(opts)
	if err != nil {
		t.Fatalf("new supervisor: %v", err)
	}
	return &fixture{
		t:         t,
		store:     store,
		runtime:   runtime,
		publisher: publisher,
		clock:     clock,
		sup:       sup,
	}
}

func (f *fixture) messageTo(agentID, id string, from domain.Address, kind domain.MessageKind, body string) domain.Message {
	f.t.Helper()
	return f.store.addMessage(domain.Message{
		ID:      id,
		HiveID:  testHiveID,
		From:    from,
		To:      domain.ForagerAddress(agentID),
		Kind:    kind,
		ChainID: "chain-1",
		Body:    body,
	})
}

func (f *fixture) taskMessage(id, body string) domain.Message {
	return f.messageTo(testAgentID, id, domain.LocalAddress("token-1"), domain.MessageTask, body)
}

func (f *fixture) notify(m domain.Message) {
	f.t.Helper()
	if err := f.sup.Notify(context.Background(), m); err != nil {
		f.t.Fatalf("notify: %v", err)
	}
}

func (f *fixture) wantState(agentID string, want domain.AgentState) {
	f.t.Helper()
	eventually(f.t, 5*time.Second, func() bool {
		return f.store.state(agentID) == want
	}, "agent "+agentID+" state did not become "+string(want))
}

func TestFirstMessageStartsCellRunsTurnAndParks(t *testing.T) {
	f := newFixture(t, nil)
	f.runtime.script(testAgentID).updates = []cell.Update{
		{Kind: cell.UpdateText, Text: "Implemented the widget fix."},
	}
	msg := f.taskMessage("m1", "Fix the widget")
	f.notify(msg)
	f.wantState(testAgentID, domain.AgentParked)

	starts := f.runtime.startsFor(testAgentID)
	if len(starts) != 1 {
		t.Fatalf("Start calls = %d, want 1", len(starts))
	}
	spec := starts[0]
	if spec.SessionRef != "" {
		t.Errorf("first Start SessionRef = %q, want empty", spec.SessionRef)
	}
	if spec.MCPToken != "agent-token-1" {
		t.Errorf("MCPToken = %q, want agent-token-1", spec.MCPToken)
	}
	if spec.MCPURL != "http://mcp.internal/mcp" {
		t.Errorf("MCPURL = %q", spec.MCPURL)
	}
	if spec.Model != "deepseek/deepseek-chat" {
		t.Errorf("Model = %q", spec.Model)
	}
	if spec.Branch != testBranch || spec.Repository != "acme/widgets" {
		t.Errorf("spec branch/repo = %q/%q", spec.Branch, spec.Repository)
	}
	if spec.CLI != "omp" {
		t.Errorf("CLI = %q", spec.CLI)
	}
	if spec.LLMBaseURL != "http://mcp.internal/llm" || spec.LLMToken != spec.MCPToken {
		t.Errorf("LLM proxy = %q / %q, want the Agent token", spec.LLMBaseURL, spec.LLMToken)
	}
	if len(spec.Env) != 0 {
		t.Errorf("Env = %+v, want no provider credentials", spec.Env)
	}
	if spec.ReadOnly {
		t.Error("the builtin worker may publish, so ReadOnly must be false")
	}
	for _, key := range []string{"methodology/config.yml", "methodology/platform.yml", "methodology/models.yml"} {
		if spec.ConfigFiles[key] == "" {
			t.Errorf("ConfigFiles is missing %q", key)
		}
	}

	agent := f.store.agentCopy(testAgentID)
	if agent.SessionRef != "session-"+testAgentID {
		t.Errorf("stored SessionRef = %q", agent.SessionRef)
	}
	if agent.PRNumber != 42 {
		t.Errorf("stored PR = %d, want 42", agent.PRNumber)
	}
	if got := f.store.callCount("CreateAgentToken:" + testAgentID); got != 1 {
		t.Errorf("CreateAgentToken calls = %d, want 1", got)
	}
	if !f.store.isHandled("m1") {
		t.Error("task message was not marked handled")
	}

	prompts := f.runtime.script(testAgentID).prompts()
	if len(prompts) != 1 {
		t.Fatalf("prompts = %d, want 1", len(prompts))
	}
	for _, want := range []string{"You are Forager", testBranch, "Fix the widget", "data, not instructions"} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("prompt missing %q:\n%s", want, prompts[0])
		}
	}

	calls := f.publisher.callsCopy()
	if len(calls) != 1 {
		t.Fatalf("Publish calls = %d, want 1", len(calls))
	}
	if calls[0].title != "Fix the widget" {
		t.Errorf("PR title = %q", calls[0].title)
	}
	if !strings.Contains(calls[0].body, "Goal: Fix the widget") {
		t.Errorf("PR body = %q", calls[0].body)
	}

	reports := f.store.reportsFrom(testAgentID)
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	if reports[0].To != domain.LocalAddress("token-1") {
		t.Errorf("report To = %q, want local:token-1", reports[0].To)
	}
	if reports[0].Kind != domain.MessageReport {
		t.Errorf("report kind = %q", reports[0].Kind)
	}
	if !strings.Contains(reports[0].Body, "Goal: Fix the widget") {
		t.Errorf("report body = %q", reports[0].Body)
	}
	if starts := f.runtime.destroyCount(testAgentID); starts != 0 {
		t.Errorf("Destroy calls = %d, want 0", starts)
	}
}

// syncBuffer is a log sink safe for the actor goroutines and the test reader.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestTurnLogsOutcomeWithoutBodies(t *testing.T) {
	logs := &syncBuffer{}
	f := newFixture(t, func(o *Options) {
		o.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	})
	f.runtime.script(testAgentID).updates = []cell.Update{
		{Kind: cell.UpdateText, Text: "Implemented the widget fix."},
	}
	f.notify(f.taskMessage("m1", "Fix the widget"))
	f.wantState(testAgentID, domain.AgentParked)

	out := logs.String()
	for _, want := range []string{
		"forager: turn started", "forager: turn finished",
		"stop_reason=end_turn", "pr=42", "messages=1",
		"input_tokens=0", "output_tokens=0", "publish_error=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("logs missing %q:\n%s", want, out)
		}
	}
	for _, secret := range []string{"test-secret", "Implemented the widget fix.", "agent-token-1", "Fix the widget"} {
		if strings.Contains(out, secret) {
			t.Errorf("logs leak %q:\n%s", secret, out)
		}
	}
}

func TestTurnWithoutOutputReportsStopReason(t *testing.T) {
	f := newFixture(t, nil)
	agent := f.store.agentCopy(testAgentID)
	agent.Handoff = nil
	f.store.setAgent(agent)

	f.notify(f.taskMessage("m1", "Fix the widget"))
	f.wantState(testAgentID, domain.AgentParked)

	reports := f.store.reportsFrom(testAgentID)
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	want := "Turn finished (stop reason: end_turn) with no output."
	if reports[0].Body != want {
		t.Fatalf("report body = %q, want %q", reports[0].Body, want)
	}
}

func TestMessagesDuringTurnMergeIntoNextTurn(t *testing.T) {
	f := newFixture(t, nil)
	script := f.runtime.script(testAgentID)
	script.blockPrompts = 1
	f.notify(f.taskMessage("m1", "first job"))
	eventually(t, 5*time.Second, func() bool { return script.promptTotal() == 1 }, "first turn did not start")

	f.notify(f.taskMessage("m2", "second job"))
	f.notify(f.taskMessage("m3", "third job"))
	script.releasePrompt()

	eventually(t, 5*time.Second, func() bool {
		return script.promptTotal() == 2 && f.store.state(testAgentID) == domain.AgentParked
	}, "second turn did not finish")

	prompts := script.prompts()
	if len(prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(prompts))
	}
	for _, want := range []string{"second job", "third job"} {
		if !strings.Contains(prompts[1], want) {
			t.Errorf("merged prompt missing %q:\n%s", want, prompts[1])
		}
	}
	for _, id := range []string{"m1", "m2", "m3"} {
		if !f.store.isHandled(id) {
			t.Errorf("message %s was not marked handled", id)
		}
	}
	if starts := f.runtime.startsFor(testAgentID); len(starts) != 2 {
		t.Fatalf("Start calls = %d, want 2", len(starts))
	}
}

func TestCancelControlInterruptsTurnAndCleansUp(t *testing.T) {
	f := newFixture(t, nil)
	script := f.runtime.script(testAgentID)
	script.blockPrompts = 1
	f.notify(f.taskMessage("m1", "long job"))
	eventually(t, 5*time.Second, func() bool { return script.promptTotal() == 1 }, "turn did not start")

	cancel := f.messageTo(testAgentID, "c1", domain.LocalAddress("token-1"), domain.MessageControl, "cancel")
	f.notify(cancel)
	f.wantState(testAgentID, domain.AgentCancelled)

	if got := script.cancelTotal(); got != 1 {
		t.Errorf("session Cancel calls = %d, want 1", got)
	}
	if got := f.runtime.destroyCount(testAgentID); got != 1 {
		t.Errorf("Destroy calls = %d, want 1", got)
	}
	if got := f.store.callCount("RevokeAgentTokens:" + testAgentID); got != 1 {
		t.Errorf("RevokeAgentTokens calls = %d, want 1", got)
	}
	if f.store.tokenCount() != 0 {
		t.Errorf("tokens left = %d, want 0", f.store.tokenCount())
	}
	if !f.store.isHandled("m1") || !f.store.isHandled("c1") {
		t.Error("consumed and control messages should both be handled")
	}
	reports := f.store.reportsFrom(testAgentID)
	if len(reports) != 1 || reports[0].To != domain.LocalAddress("token-1") {
		t.Fatalf("reports = %+v, want one report to local:token-1", reports)
	}
	if !strings.Contains(reports[0].Body, "cancelled") {
		t.Errorf("report body = %q", reports[0].Body)
	}
}

func TestIdleStopHibernateAndWake(t *testing.T) {
	f := newFixture(t, nil)
	f.notify(f.taskMessage("m1", "first job"))
	f.wantState(testAgentID, domain.AgentParked)
	sessionRef := f.store.agentCopy(testAgentID).SessionRef
	if sessionRef == "" {
		t.Fatal("SessionRef was not stored")
	}

	f.clock.Advance(31 * time.Minute)
	if err := f.sup.Scan(context.Background()); err != nil {
		t.Fatalf("scan after idle: %v", err)
	}
	if got := f.runtime.stopCount(testAgentID); got != 1 {
		t.Errorf("Stop calls = %d, want 1", got)
	}
	if got := f.store.state(testAgentID); got != domain.AgentParked {
		t.Errorf("state after Stop = %q, want parked", got)
	}

	f.clock.Advance(25 * time.Hour)
	if err := f.sup.Scan(context.Background()); err != nil {
		t.Fatalf("scan after hibernate window: %v", err)
	}
	if got := f.runtime.removeCount(testAgentID); got != 1 {
		t.Errorf("Remove calls = %d, want 1", got)
	}
	if got := f.store.state(testAgentID); got != domain.AgentHibernated {
		t.Errorf("state after Remove = %q, want hibernated", got)
	}

	f.notify(f.taskMessage("m2", "wake up"))
	eventually(t, 5*time.Second, func() bool {
		return f.runtime.script(testAgentID).promptTotal() == 2 &&
			f.store.state(testAgentID) == domain.AgentParked
	}, "hibernated forager did not wake and park")
	starts := f.runtime.startsFor(testAgentID)
	if len(starts) != 2 {
		t.Fatalf("Start calls = %d, want 2", len(starts))
	}
	if starts[1].SessionRef != sessionRef {
		t.Errorf("wake Start SessionRef = %q, want %q", starts[1].SessionRef, sessionRef)
	}
}

func TestTurnTimeoutFailsAndCleansUp(t *testing.T) {
	f := newFixture(t, func(o *Options) {
		o.Store.(*fakeStore).setHiveConfig(testHiveID, func(c *domain.HiveConfig) {
			c.TurnTimeoutMinutes = 1
		})
	})
	script := f.runtime.script(testAgentID)
	script.blockPrompts = 1
	f.notify(f.taskMessage("m1", "slow job"))
	eventually(t, 5*time.Second, func() bool { return script.promptTotal() == 1 }, "turn did not start")
	eventually(t, 5*time.Second, func() bool { return f.clock.Pending() > 0 }, "turn timer was not registered")

	f.clock.Advance(2 * time.Minute)
	f.wantState(testAgentID, domain.AgentFailed)

	if got := script.cancelTotal(); got != 1 {
		t.Errorf("session Cancel calls = %d, want 1", got)
	}
	if got := f.runtime.destroyCount(testAgentID); got != 1 {
		t.Errorf("Destroy calls = %d, want 1", got)
	}
	if got := f.store.callCount("RevokeAgentTokens:" + testAgentID); got != 1 {
		t.Errorf("RevokeAgentTokens calls = %d, want 1", got)
	}
	reports := f.store.reportsFrom(testAgentID)
	if len(reports) != 1 || !strings.Contains(reports[0].Body, "timed out") {
		t.Fatalf("reports = %+v, want a timeout report", reports)
	}
}

func TestLifetimeExceededFailsAndCleansUp(t *testing.T) {
	f := newFixture(t, nil)
	agent := f.store.agentCopy(testAgentID)
	agent.CreatedAt = f.clock.Now().Add(-200 * time.Hour)
	f.store.setAgent(agent)

	if err := f.sup.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	f.wantState(testAgentID, domain.AgentFailed)
	if got := f.runtime.destroyCount(testAgentID); got != 1 {
		t.Errorf("Destroy calls = %d, want 1", got)
	}
	if got := f.store.callCount("RevokeAgentTokens:" + testAgentID); got != 1 {
		t.Errorf("RevokeAgentTokens calls = %d, want 1", got)
	}
	reports := f.store.reportsFrom(testAgentID)
	if len(reports) != 1 || reports[0].To != domain.AddressHive {
		t.Fatalf("reports = %+v, want one report to hive", reports)
	}
	if !strings.Contains(reports[0].Body, "lifetime") {
		t.Errorf("report body = %q", reports[0].Body)
	}
	if script := f.runtime.script(testAgentID); script.promptTotal() != 0 {
		t.Errorf("Prompt calls = %d, want 0", script.promptTotal())
	}
}

func TestStartFailureRetriesThenFails(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.MaxFailures = 3 })
	boom := errors.New("docker unavailable")
	f.runtime.script(testAgentID).startErrs = []error{boom, boom, boom}
	f.notify(f.taskMessage("m1", "fix it"))
	f.wantState(testAgentID, domain.AgentFailed)

	if got := len(f.runtime.startsFor(testAgentID)); got != 3 {
		t.Errorf("Start calls = %d, want 3", got)
	}
	if got := f.runtime.script(testAgentID).promptTotal(); got != 0 {
		t.Errorf("Prompt calls = %d, want 0", got)
	}
	if got := f.runtime.destroyCount(testAgentID); got != 1 {
		t.Errorf("Destroy calls = %d, want 1", got)
	}
	if got := f.store.callCount("RevokeAgentTokens:" + testAgentID); got != 1 {
		t.Errorf("RevokeAgentTokens calls = %d, want 1", got)
	}
	reports := f.store.reportsFrom(testAgentID)
	if len(reports) != 1 || !strings.Contains(reports[0].Body, "failed to start") {
		t.Fatalf("reports = %+v, want a start-failure report", reports)
	}
	if !f.store.isHandled("m1") {
		t.Error("message was not marked handled after the Forager failed")
	}
}

func TestDuplicateNotificationDoesNotReplayHandledMessage(t *testing.T) {
	f := newFixture(t, nil)
	msg := f.taskMessage("m1", "fix it")
	f.notify(msg)
	f.wantState(testAgentID, domain.AgentParked)

	// A redelivery of the same message must not start another turn.
	f.notify(msg)
	time.Sleep(50 * time.Millisecond)
	if got := f.runtime.script(testAgentID).promptTotal(); got != 1 {
		t.Errorf("Prompt calls = %d, want 1", got)
	}
	if got := len(f.runtime.startsFor(testAgentID)); got != 1 {
		t.Errorf("Start calls = %d, want 1", got)
	}
}

func TestPromptRetriesTransientFailure(t *testing.T) {
	f := newFixture(t, nil)
	script := f.runtime.script(testAgentID)
	script.promptErrs = []error{errors.New("connection reset")}
	f.notify(f.taskMessage("m1", "fix it"))
	f.wantState(testAgentID, domain.AgentParked)

	if got := script.promptTotal(); got != 2 {
		t.Errorf("Prompt calls = %d, want 2", got)
	}
	if !f.store.isHandled("m1") {
		t.Error("message was not marked handled after the retry succeeded")
	}
	if got := f.publisher.callsCopy(); len(got) != 1 {
		t.Errorf("Publish calls = %d, want 1", len(got))
	}
}

func TestOneStuckForagerDoesNotBlockAnother(t *testing.T) {
	f := newFixture(t, nil)
	const secondAgentID = "agent-2"
	f.store.addAgent(domain.Agent{
		ID:      secondAgentID,
		HiveID:  testHiveID,
		Role:    domain.RoleForager,
		State:   domain.AgentCreating,
		CLI:     "omp",
		Task:    "Second job",
		Branch:  "swarm/hive-1/agent-2",
		Handoff: &domain.Handoff{Goal: "Second job"},
	})
	slow := f.runtime.script(testAgentID)
	slow.blockPrompts = 1

	f.notify(f.taskMessage("m1", "slow job"))
	eventually(t, 5*time.Second, func() bool { return slow.promptTotal() == 1 }, "slow turn did not start")

	f.notify(f.messageTo(secondAgentID, "m2", domain.LocalAddress("token-1"), domain.MessageTask, "second job"))
	f.wantState(secondAgentID, domain.AgentParked)
	if got := f.store.state(testAgentID); got == domain.AgentParked || got == domain.AgentFailed {
		t.Errorf("slow agent state = %q, want still in flight", got)
	}

	f.notify(f.messageTo(testAgentID, "c1", domain.LocalAddress("token-1"), domain.MessageControl, "cancel"))
	f.wantState(testAgentID, domain.AgentCancelled)
	if got := f.store.state(secondAgentID); got != domain.AgentParked {
		t.Errorf("second agent state = %q, want parked", got)
	}
}

func TestScanRecoversUnhandledMessagesAfterRestart(t *testing.T) {
	f := newFixture(t, nil)
	agent := f.store.agentCopy(testAgentID)
	agent.State = domain.AgentParked
	agent.SessionRef = "session-existing"
	f.store.setAgent(agent)
	f.store.addMessage(domain.Message{
		ID:      "m1",
		HiveID:  testHiveID,
		From:    domain.LocalAddress("token-1"),
		To:      domain.ForagerAddress(testAgentID),
		Kind:    domain.MessageTask,
		ChainID: "chain-1",
		Body:    "resume this",
	})

	if err := f.sup.Scan(context.Background()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	eventually(t, 5*time.Second, func() bool {
		return f.runtime.script(testAgentID).promptTotal() == 1 && f.store.isHandled("m1")
	}, "scan did not recover the unhandled message")

	starts := f.runtime.startsFor(testAgentID)
	if len(starts) != 1 || starts[0].SessionRef != "session-existing" {
		t.Fatalf("starts = %+v, want one resume with session-existing", starts)
	}
	if !strings.Contains(f.runtime.script(testAgentID).prompts()[0], "resume this") {
		t.Error("recovered prompt did not include the message")
	}
	f.wantState(testAgentID, domain.AgentParked)
}

func TestBuildPromptCapsLengthAndFoldsMessages(t *testing.T) {
	agent := domain.Agent{ID: "agent-1", Task: "task", Branch: testBranch}
	hive := domain.Hive{ID: testHiveID, Name: "Acme", Repository: "acme/widgets"}
	msgs := make([]domain.Message, 0, 100)
	for i := 0; i < 100; i++ {
		msgs = append(msgs, domain.Message{
			ID:   fmt.Sprintf("m%d", i),
			Kind: domain.MessageTask,
			From: domain.LocalAddress("token-1"),
			Body: strings.Repeat("x", 500),
		})
	}
	text := buildPrompt(agent, hive, msgs, true)
	if len([]rune(text)) > MaxPromptChars+100 {
		t.Errorf("prompt length = %d, want near %d", len([]rune(text)), MaxPromptChars)
	}
	if !strings.Contains(text, "more messages omitted") {
		t.Error("prompt did not fold the omitted messages")
	}
}

func TestRoleSelectsModelRevisionAndReadOnly(t *testing.T) {
	f := newFixture(t, nil)
	const revision = "0123456789abcdef0123456789abcdef01234567"
	f.store.mu.Lock()
	agent := f.store.agents[testAgentID]
	agent.RoleName = "verifier"
	agent.Revision = revision
	f.store.agents[testAgentID] = agent
	f.store.methodology = &domain.Methodology{Manifest: domain.MethodologyManifest{
		DefaultRole: "worker",
		Roles: map[string]domain.Role{
			"worker": {Permissions: domain.RolePermissions{Publish: true, Message: true}},
			"verifier": {
				Model:     "openai/gpt-5",
				Prompt:    "roles/verifier.md",
				DenyTools: []string{"edit", "write"},
				Permissions: domain.RolePermissions{
					Publish: false,
					Message: true,
					Attest:  true,
				},
			},
		},
	}}
	f.store.methodologyFiles = []domain.MethodologyFile{
		{Path: "roles/verifier.md", Content: "You verify one revision."},
	}
	f.store.mu.Unlock()

	f.runtime.script(testAgentID).updates = []cell.Update{{Kind: cell.UpdateText, Text: "verified"}}
	f.notify(f.taskMessage("m1", "Verify the change"))
	f.wantState(testAgentID, domain.AgentParked)

	starts := f.runtime.startsFor(testAgentID)
	if len(starts) != 1 {
		t.Fatalf("Start calls = %d, want 1", len(starts))
	}
	spec := starts[0]
	if spec.Model != "openai/gpt-5" {
		t.Errorf("Model = %q, want the role model", spec.Model)
	}
	if spec.Revision != revision {
		t.Errorf("Revision = %q, want %q", spec.Revision, revision)
	}
	if !spec.ReadOnly {
		t.Error("a role without publish permission must produce a read-only Spec")
	}
	if got := spec.ConfigFiles["methodology/ROLE.md"]; got != "You verify one revision." {
		t.Errorf("ROLE.md = %q", got)
	}
	if overlay := spec.ConfigFiles["methodology/platform.yml"]; !strings.Contains(overlay, `"edit": deny`) ||
		!strings.Contains(overlay, `"write": deny`) {
		t.Errorf("denyTools missing from overlay:\n%s", overlay)
	}
	if models := spec.ConfigFiles["methodology/models.yml"]; !strings.Contains(models, `"openai":`) ||
		!strings.Contains(models, `"gpt-5"`) {
		t.Errorf("models.yml did not use the role model:\n%s", models)
	}
}

func TestUnknownMethodologyRoleFailsTheTurn(t *testing.T) {
	f := newFixture(t, nil)
	f.store.mu.Lock()
	agent := f.store.agents[testAgentID]
	agent.RoleName = "missing"
	f.store.agents[testAgentID] = agent
	f.store.methodology = &domain.Methodology{Manifest: domain.MethodologyManifest{
		DefaultRole: "worker",
		Roles:       map[string]domain.Role{"worker": {Permissions: domain.RolePermissions{Publish: true}}},
	}}
	f.store.mu.Unlock()

	f.notify(f.taskMessage("m1", "Do work"))
	f.wantState(testAgentID, domain.AgentFailed)
	if starts := f.runtime.startsFor(testAgentID); len(starts) != 0 {
		t.Fatalf("Start ran with an unknown role: %+v", starts)
	}
}

// finish_task sends the Agent a "complete" control message during its turn;
// the turn finishes normally, then the Agent is completed and cleaned up.
func TestCompleteControlEndsAgentAfterTurn(t *testing.T) {
	f := newFixture(t, nil)
	f.notify(f.taskMessage("m1", "verify the PR"))
	f.wantState(testAgentID, domain.AgentParked)

	done := f.messageTo(testAgentID, "c1", domain.ForagerAddress(testAgentID), domain.MessageControl, "complete\nattested PASS")
	f.notify(done)
	f.wantState(testAgentID, domain.AgentCompleted)

	if got := f.runtime.destroyCount(testAgentID); got != 1 {
		t.Errorf("Destroy calls = %d, want 1", got)
	}
	if f.store.tokenCount() != 0 {
		t.Errorf("tokens left = %d, want 0", f.store.tokenCount())
	}
	if !f.store.isHandled("c1") {
		t.Error("complete control message should be handled")
	}
}
