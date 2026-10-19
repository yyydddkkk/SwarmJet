package verify

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func fixture(t *testing.T, cfg domain.VerifyConfig) (*fakeStore, domain.Hive, domain.Agent) {
	t.Helper()
	store := newFakeStore()
	hive := store.addHive(domain.Hive{
		Repository: "acme/hive",
		Config:     domain.HiveConfig{Verify: cfg},
	})
	owner := store.addAgent(domain.Agent{
		HiveID:  hive.ID,
		Role:    domain.RoleForager,
		State:   domain.AgentParked,
		ChainID: "chain-owner",
		Task:    "make the retry helper deterministic",
	})
	return store, hive, owner
}

func TestHeadChangedOffModeDoesNothing(t *testing.T) {
	store, hive, owner := fixture(t, domain.VerifyConfig{Mode: "off"})
	store.mu.Lock()
	store.verdicts = append(store.verdicts, domain.Verdict{
		HiveID: hive.ID, PRNumber: 7, HeadSHA: "old", OwnerID: owner.ID, Result: domain.VerdictFail,
	})
	store.mu.Unlock()

	d := New(Options{Store: store})
	if err := d.HeadChanged(context.Background(), hive, owner, 7, "new"); err != nil {
		t.Fatalf("HeadChanged: %v", err)
	}
	if got := len(store.allAgents()); got != 1 {
		t.Fatalf("agents = %d, want only the owner", got)
	}
	if got := len(store.allMessages()); got != 0 {
		t.Fatalf("messages = %d, want 0", got)
	}
	for _, v := range store.allVerdicts() {
		if v.Superseded {
			t.Fatal("off mode superseded an existing verdict")
		}
	}
}

func TestHeadChangedSupersedesCancelsCreatesAndIsIdempotent(t *testing.T) {
	store, hive, owner := fixture(t, domain.VerifyConfig{MaxRounds: 3})
	oldVerifier := store.addAgent(domain.Agent{
		HiveID:    hive.ID,
		State:     domain.AgentActive,
		Kind:      domain.KindVerifier,
		PRNumber:  7,
		VerifySHA: "old-sha",
		OwnerID:   owner.ID,
		ChainID:   owner.ChainID,
	})
	store.mu.Lock()
	store.verdicts = append(store.verdicts,
		domain.Verdict{HiveID: hive.ID, PRNumber: 7, HeadSHA: "old-sha", OwnerID: owner.ID, VerifierID: oldVerifier.ID, Result: domain.VerdictFail},
		domain.Verdict{HiveID: hive.ID, PRNumber: 8, HeadSHA: "other-pr", OwnerID: owner.ID, Result: domain.VerdictFail},
	)
	store.mu.Unlock()

	d := New(Options{Store: store})
	if err := d.HeadChanged(context.Background(), hive, owner, 7, "new-sha"); err != nil {
		t.Fatalf("HeadChanged: %v", err)
	}

	var created domain.Agent
	for _, a := range store.allAgents() {
		if a.Kind == domain.KindVerifier && a.VerifySHA == "new-sha" {
			created = a
		}
	}
	if created.ID == "" {
		t.Fatal("no verifier was created for the new head")
	}
	if created.PRNumber != 7 || created.OwnerID != owner.ID || created.ChainID != owner.ChainID {
		t.Fatalf("verifier = %+v, want PR/owner/chain from the owner", created)
	}
	if created.Branch != "verify/"+created.ID {
		t.Fatalf("branch = %q, want verify/%s", created.Branch, created.ID)
	}
	if !strings.Contains(created.Task, "new-sha") || !strings.Contains(created.Task, "submit_verdict") {
		t.Fatalf("task does not tell the verifier what to check: %q", created.Task)
	}

	tasks := store.messagesTo(hive.ID, domain.ForagerAddress(created.ID))
	if len(tasks) != 1 || tasks[0].Kind != domain.MessageTask {
		t.Fatalf("verifier task messages = %+v, want exactly one task", tasks)
	}
	cancels := store.messagesTo(hive.ID, domain.ForagerAddress(oldVerifier.ID))
	if len(cancels) != 1 || cancels[0].Kind != domain.MessageControl || cancels[0].Body != "cancel" {
		t.Fatalf("old verifier messages = %+v, want one cancel control", cancels)
	}

	for _, v := range store.allVerdicts() {
		if v.PRNumber == 7 && v.HeadSHA == "old-sha" && !v.Superseded {
			t.Fatal("old-head verdict was not superseded")
		}
		if v.PRNumber == 8 && v.Superseded {
			t.Fatal("a verdict for another PR was superseded")
		}
	}

	// Repeating the same webhook must not create a second verifier or task.
	if err := d.HeadChanged(context.Background(), hive, owner, 7, "new-sha"); err != nil {
		t.Fatalf("HeadChanged retry: %v", err)
	}
	if got := len(store.allAgents()); got != 3 {
		t.Fatalf("agents after retry = %d, want owner + old verifier + one new verifier", got)
	}
	if got := len(store.messagesTo(hive.ID, domain.ForagerAddress(created.ID))); got != 1 {
		t.Fatalf("verifier task messages after retry = %d, want 1", got)
	}
	if got := len(store.messagesTo(hive.ID, domain.ForagerAddress(oldVerifier.ID))); got != 1 {
		t.Fatalf("cancel messages after retry = %d, want 1", got)
	}
}

func TestHeadChangedStopsAtMaxRounds(t *testing.T) {
	store, hive, owner := fixture(t, domain.VerifyConfig{MaxRounds: 2})
	dispatcher := domain.LocalAddress("dispatch")
	if _, err := store.SendMessage(context.Background(), domain.Message{
		HiveID: hive.ID, From: dispatcher, To: domain.ForagerAddress(owner.ID), Kind: domain.MessageTask, Body: "do it",
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := store.RecordVerdict(context.Background(), domain.Verdict{
			HiveID: hive.ID, PRNumber: 7, HeadSHA: "old", OwnerID: owner.ID, VerifierID: fmt.Sprintf("v%d", i), Result: domain.VerdictFail,
		}); err != nil {
			t.Fatalf("seed verdict: %v", err)
		}
	}

	d := New(Options{Store: store})
	if err := d.HeadChanged(context.Background(), hive, owner, 7, "new-sha"); err != nil {
		t.Fatalf("HeadChanged: %v", err)
	}
	for _, a := range store.allAgents() {
		if a.Kind == domain.KindVerifier {
			t.Fatalf("a verifier was created after the round limit: %+v", a)
		}
	}
	reports := store.messagesTo(hive.ID, dispatcher)
	if len(reports) != 1 || reports[0].Kind != domain.MessageReport || !strings.Contains(reports[0].Body, "停止自动验证") {
		t.Fatalf("dispatcher messages = %+v, want one round-limit report", reports)
	}
	controls := store.messagesTo(hive.ID, domain.ForagerAddress(owner.ID))
	if len(controls) != 2 { // seeded task + control
		t.Fatalf("owner messages = %+v, want the task plus one control", controls)
	}
	var control *domain.Message
	for i := range controls {
		if controls[i].Kind == domain.MessageControl {
			control = &controls[i]
		}
	}
	if control == nil || !strings.Contains(control.Body, "停止自动验证") {
		t.Fatalf("owner control = %+v, want a round-limit control", control)
	}

	// The notification is idempotent for the same (PR, SHA).
	if err := d.HeadChanged(context.Background(), hive, owner, 7, "new-sha"); err != nil {
		t.Fatalf("HeadChanged retry: %v", err)
	}
	if got := len(store.messagesTo(hive.ID, dispatcher)); got != 1 {
		t.Fatalf("dispatcher messages after retry = %d, want 1", got)
	}
	if got := len(store.messagesTo(hive.ID, domain.ForagerAddress(owner.ID))); got != 2 {
		t.Fatalf("owner messages after retry = %d, want 2", got)
	}
}

func TestSubmitVerdictDeliversEachResultToTheRightPlace(t *testing.T) {
	cases := []struct {
		result       domain.VerdictResult
		target       domain.Address
		kind         domain.MessageKind
		wantText     string
		ownerMessage bool
	}{
		{domain.VerdictFail, domain.ForagerAddress("owner"), domain.MessageTask, "请修复", true},
		{domain.VerdictPass, domain.LocalAddress("dispatch"), domain.MessageReport, "CI 通过后即可合并", false},
		{domain.VerdictPassNotes, domain.LocalAddress("dispatch"), domain.MessageReport, "CI 通过后即可合并", false},
		{domain.VerdictBlocked, domain.LocalAddress("dispatch"), domain.MessageReport, "BLOCKED", false},
	}

	for _, tc := range cases {
		t.Run(string(tc.result), func(t *testing.T) {
			store, hive, owner := fixture(t, domain.VerifyConfig{})
			dispatcher := domain.LocalAddress("dispatch")
			if _, err := store.SendMessage(context.Background(), domain.Message{
				HiveID: hive.ID, From: dispatcher, To: domain.ForagerAddress(owner.ID), Kind: domain.MessageTask, Body: "do it",
			}); err != nil {
				t.Fatalf("seed task: %v", err)
			}
			verifier := store.addAgent(domain.Agent{
				HiveID:    hive.ID,
				State:     domain.AgentActive,
				Kind:      domain.KindVerifier,
				PRNumber:  7,
				VerifySHA: "head-sha",
				OwnerID:   owner.ID,
				ChainID:   owner.ChainID,
			})
			gh := &fakeGitHub{head: "head-sha"}
			var seen []domain.Verdict
			d := New(Options{
				Store:  store,
				GitHub: gh,
				OnVerdict: func(_ context.Context, _ domain.Hive, v domain.Verdict) error {
					seen = append(seen, v)
					return nil
				},
			})

			verdict, superseded, err := d.SubmitVerdict(context.Background(), verifier, tc.result, "the summary", []domain.Finding{{
				Severity: "critical", Location: "pkg/retry.go", Finding: "the clock is still read", Evidence: "test output",
			}})
			if err != nil {
				t.Fatalf("SubmitVerdict: %v", err)
			}
			if superseded {
				t.Fatal("fresh verdict reported as superseded")
			}
			if verdict.CommentURL == "" {
				t.Fatal("verdict has no comment URL")
			}
			if len(gh.comments) != 1 {
				t.Fatalf("comments = %d, want 1", len(gh.comments))
			}
			comment := gh.comments[0]
			for _, want := range []string{"head-sha", "the summary", "pkg/retry.go", "验证 Forager 生成"} {
				if !strings.Contains(comment, want) {
					t.Fatalf("comment does not contain %q:\n%s", want, comment)
				}
			}
			if len(seen) != 1 || seen[0].ID != verdict.ID {
				t.Fatalf("OnVerdict calls = %+v, want the fresh verdict", seen)
			}

			target := tc.target
			if target == domain.ForagerAddress("owner") {
				target = domain.ForagerAddress(owner.ID)
			}
			messages := store.messagesTo(hive.ID, target)
			var got *domain.Message
			for i := range messages {
				if messages[i].Kind == tc.kind {
					got = &messages[i]
				}
			}
			if got == nil {
				t.Fatalf("no %s message to %s: %+v", tc.kind, target, messages)
			}
			if !strings.Contains(got.Body, tc.wantText) || !strings.Contains(got.Body, verdict.CommentURL) {
				t.Fatalf("message body = %q, want %q and the comment URL", got.Body, tc.wantText)
			}
			if !tc.ownerMessage {
				if ownerMessages := store.messagesTo(hive.ID, domain.ForagerAddress(owner.ID)); len(ownerMessages) != 1 {
					t.Fatalf("owner messages = %+v, want only the seeded task", ownerMessages)
				}
			}
		})
	}
}

func TestSubmitVerdictStaleSHAIsRecordedSuperseded(t *testing.T) {
	store, hive, owner := fixture(t, domain.VerifyConfig{})
	verifier := store.addAgent(domain.Agent{
		HiveID:    hive.ID,
		State:     domain.AgentActive,
		Kind:      domain.KindVerifier,
		PRNumber:  7,
		VerifySHA: "old-sha",
		OwnerID:   owner.ID,
	})
	gh := &fakeGitHub{head: "new-sha"}
	d := New(Options{Store: store, GitHub: gh})

	verdict, superseded, err := d.SubmitVerdict(context.Background(), verifier, domain.VerdictFail, "a stale failure", nil)
	if err != nil {
		t.Fatalf("SubmitVerdict: %v", err)
	}
	if !superseded {
		t.Fatal("stale verdict was not reported as superseded")
	}
	if !verdict.Superseded {
		t.Fatal("stored verdict is not marked superseded")
	}
	if len(gh.comments) != 0 {
		t.Fatalf("stale verdict posted %d comments, want 0", len(gh.comments))
	}
	if got := len(store.allMessages()); got != 0 {
		t.Fatalf("stale verdict sent %d messages, want 0", got)
	}
}

func TestRenderCommentTruncatesVerifierText(t *testing.T) {
	findings := make([]domain.Finding, maxFindings+1)
	for i := range findings {
		findings[i] = domain.Finding{Severity: "critical", Finding: strings.Repeat("B", 1500)}
	}
	verdict := domain.Verdict{
		PRNumber: 7,
		HeadSHA:  "abc123",
		Result:   domain.VerdictFail,
		Summary:  strings.Repeat("A", maxSummaryRunes+500),
		Findings: findings,
	}
	comment := RenderComment(verdict)
	if !strings.Contains(comment, strings.Repeat("A", maxSummaryRunes-1)+"…") {
		t.Fatal("summary was not truncated to the 2000-character budget")
	}
	if strings.Contains(comment, strings.Repeat("A", maxSummaryRunes)) {
		t.Fatal("summary kept more than the 2000-character budget")
	}
	if got := strings.Count(comment, "- ["); got != maxFindings {
		t.Fatalf("rendered findings = %d, want %d", got, maxFindings)
	}
	for _, line := range strings.Split(comment, "\n") {
		if strings.HasPrefix(line, "- [") && utf8.RuneCountInString(line) > maxFindingRunes {
			t.Fatalf("finding line has %d characters, want at most %d", utf8.RuneCountInString(line), maxFindingRunes)
		}
	}

	message := failMessage(domain.Hive{Repository: "acme/hive"}, verdict, "")
	if got := utf8.RuneCountInString(message); got > domain.MaxMessageBody {
		t.Fatalf("FAIL message has %d characters, want at most %d", got, domain.MaxMessageBody)
	}
	if strings.Contains(message, strings.Repeat("B", maxFindingRunes+1)) {
		t.Fatal("FAIL message kept an over-long finding")
	}
}

func TestSubmitVerdictRejectsNonVerifier(t *testing.T) {
	store, hive, owner := fixture(t, domain.VerifyConfig{})
	worker := store.addAgent(domain.Agent{
		HiveID: hive.ID, State: domain.AgentActive, Kind: domain.KindWorker, PRNumber: 7, VerifySHA: "sha", OwnerID: owner.ID,
	})
	d := New(Options{Store: store, GitHub: &fakeGitHub{head: "sha"}})
	if _, _, err := d.SubmitVerdict(context.Background(), worker, domain.VerdictPass, "summary", nil); err == nil {
		t.Fatal("worker verdict was accepted")
	}
}
