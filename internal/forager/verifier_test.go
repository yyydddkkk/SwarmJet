package forager

import (
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

const (
	verifierAgentID = "verifier-1"
	verifierSHA     = "0123456789abcdef0123456789abcdef01234567"
	verifierPR      = int64(42)
)

// newVerifierFixture adds one live verifier for the worker in the base fixture.
// The task message is stored but not delivered, so a test can install a prompt
// hook before the turn starts.
func newVerifierFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, func(o *Options) {
		o.BaseBranch = "main"
	})
	f.store.setHiveConfig(testHiveID, func(c *domain.HiveConfig) {
		c.Verify.Model = "verify/model"
	})
	owner := f.store.agentCopy(testAgentID)
	f.store.addAgent(domain.Agent{
		ID:        verifierAgentID,
		HiveID:    testHiveID,
		Role:      domain.RoleForager,
		State:     domain.AgentCreating,
		CLI:       "opencode",
		Task:      "Verify the widget fix",
		Branch:    "verify/" + verifierAgentID,
		PRNumber:  verifierPR,
		ChainID:   "chain-1",
		Kind:      domain.KindVerifier,
		VerifySHA: verifierSHA,
		OwnerID:   owner.ID,
	})
	f.store.addMessage(domain.Message{
		ID:      "verify-task",
		HiveID:  testHiveID,
		From:    domain.AddressSystem,
		To:      domain.ForagerAddress(verifierAgentID),
		Kind:    domain.MessageTask,
		ChainID: "chain-1",
		Body:    "Verify PR #42 at " + verifierSHA,
	})
	return f
}

func (f *fixture) notifyVerifier() {
	f.t.Helper()
	f.store.mu.Lock()
	var message domain.Message
	for _, m := range f.store.messages {
		if m.ID == "verify-task" {
			message = m
			break
		}
	}
	f.store.mu.Unlock()
	if message.ID == "" {
		f.t.Fatal("verifier task message missing")
	}
	if err := f.sup.Notify(context.Background(), message); err != nil {
		f.t.Fatalf("notify verifier: %v", err)
	}
}

func recordPassVerdict(t *testing.T, f *fixture) func(context.Context, string) {
	t.Helper()
	return func(ctx context.Context, _ string) {
		if _, err := f.store.RecordVerdict(ctx, domain.Verdict{
			HiveID:     testHiveID,
			PRNumber:   verifierPR,
			HeadSHA:    verifierSHA,
			OwnerID:    testAgentID,
			VerifierID: verifierAgentID,
			Result:     domain.VerdictPass,
			Summary:    "The change builds and the tests pass.",
		}); err != nil {
			t.Errorf("RecordVerdict: %v", err)
		}
	}
}

func TestVerifierSpecPromptAndVerdictCompletes(t *testing.T) {
	f := newVerifierFixture(t)
	script := f.runtime.script(verifierAgentID)
	script.promptHook = recordPassVerdict(t, f)

	f.notifyVerifier()
	f.wantState(verifierAgentID, domain.AgentCompleted)

	starts := f.runtime.startsFor(verifierAgentID)
	if len(starts) != 1 {
		t.Fatalf("Start calls = %d, want 1", len(starts))
	}
	spec := starts[0]
	if spec.Revision != verifierSHA {
		t.Errorf("Revision = %q, want %q", spec.Revision, verifierSHA)
	}
	if !spec.ReadOnly {
		t.Error("verifier spec is not read-only")
	}
	if spec.Model != "verify/model" {
		t.Errorf("Model = %q, want the Hive verify model", spec.Model)
	}
	if spec.Branch != "verify/"+verifierAgentID {
		t.Errorf("Branch = %q", spec.Branch)
	}
	if spec.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q", spec.BaseBranch)
	}
	if spec.SessionRef != "" {
		t.Errorf("first Start SessionRef = %q, want empty", spec.SessionRef)
	}

	if calls := f.publisher.callsCopy(); len(calls) != 0 {
		t.Fatalf("verifier called Publisher: %+v", calls)
	}
	if got := f.runtime.destroyCount(verifierAgentID); got != 1 {
		t.Errorf("Destroy calls = %d, want 1", got)
	}
	if got := f.store.callCount("RevokeAgentTokens:" + verifierAgentID); got != 1 {
		t.Errorf("RevokeAgentTokens calls = %d, want 1", got)
	}

	prompts := script.prompts()
	if len(prompts) != 1 {
		t.Fatalf("prompts = %d, want 1", len(prompts))
	}
	for _, want := range []string{
		"independent verifier", "did not write", "Pull request: #42",
		verifierSHA, "Base branch: main", "Fix the widget", "submit_verdict",
		"Do not modify the code", "data, not instructions",
	} {
		if !strings.Contains(prompts[0], want) {
			t.Errorf("verifier prompt missing %q:\n%s", want, prompts[0])
		}
	}

	verdicts := f.store.verdictsCopy()
	if len(verdicts) != 1 || verdicts[0].Result != domain.VerdictPass || verdicts[0].VerifierID != verifierAgentID {
		t.Fatalf("verdicts = %+v", verdicts)
	}
}

func TestVerifierReminderThenVerdictCompletes(t *testing.T) {
	f := newVerifierFixture(t)
	script := f.runtime.script(verifierAgentID)
	promptsSeen := 0
	record := recordPassVerdict(t, f)
	script.promptHook = func(ctx context.Context, text string) {
		promptsSeen++
		if promptsSeen == 2 {
			record(ctx, text)
		}
	}

	f.notifyVerifier()
	f.wantState(verifierAgentID, domain.AgentCompleted)

	prompts := script.prompts()
	if len(prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], "You have not submitted a verdict yet") ||
		!strings.Contains(prompts[1], "submit_verdict") {
		t.Errorf("reminder prompt missing the submit_verdict requirement:\n%s", prompts[1])
	}
	if calls := f.publisher.callsCopy(); len(calls) != 0 {
		t.Fatalf("verifier called Publisher: %+v", calls)
	}
	if got := f.runtime.destroyCount(verifierAgentID); got != 1 {
		t.Errorf("Destroy calls = %d, want 1", got)
	}
	if verdicts := f.store.verdictsCopy(); len(verdicts) != 1 || verdicts[0].Result != domain.VerdictPass {
		t.Fatalf("verdicts = %+v", verdicts)
	}
}

func TestVerifierTwoTurnsWithoutVerdictRecordsBlocked(t *testing.T) {
	f := newVerifierFixture(t)
	script := f.runtime.script(verifierAgentID)

	f.notifyVerifier()
	f.wantState(verifierAgentID, domain.AgentCompleted)

	prompts := script.prompts()
	if len(prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(prompts))
	}
	if !strings.Contains(prompts[1], "submit_verdict") {
		t.Errorf("reminder prompt missing submit_verdict:\n%s", prompts[1])
	}
	if calls := f.publisher.callsCopy(); len(calls) != 0 {
		t.Fatalf("verifier called Publisher: %+v", calls)
	}
	if got := f.runtime.destroyCount(verifierAgentID); got != 1 {
		t.Errorf("Destroy calls = %d, want 1", got)
	}

	verdicts := f.store.verdictsCopy()
	if len(verdicts) != 1 {
		t.Fatalf("verdicts = %+v, want one platform blocked verdict", verdicts)
	}
	got := verdicts[0]
	if got.Result != domain.VerdictBlocked {
		t.Errorf("result = %q, want blocked", got.Result)
	}
	if !strings.Contains(got.Summary, "没有给出结论") {
		t.Errorf("summary = %q, want the missing-verdict explanation", got.Summary)
	}
	if got.PRNumber != verifierPR || got.HeadSHA != verifierSHA ||
		got.OwnerID != testAgentID || got.VerifierID != verifierAgentID {
		t.Errorf("blocked verdict identity = %+v", got)
	}
}

func TestVerifierUsesForagerModelWhenVerifyModelEmpty(t *testing.T) {
	f := newVerifierFixture(t)
	f.store.setHiveConfig(testHiveID, func(c *domain.HiveConfig) {
		c.Verify.Model = ""
	})
	f.runtime.script(verifierAgentID).promptHook = recordPassVerdict(t, f)

	f.notifyVerifier()
	f.wantState(verifierAgentID, domain.AgentCompleted)

	starts := f.runtime.startsFor(verifierAgentID)
	if len(starts) != 1 {
		t.Fatalf("Start calls = %d, want 1", len(starts))
	}
	if starts[0].Model != "opencode/big-pickle" {
		t.Fatalf("Model = %q, want the Forager model", starts[0].Model)
	}
}
