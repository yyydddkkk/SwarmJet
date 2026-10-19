package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
)

type fakeMergeService struct {
	readiness merge.Readiness
	checkErr  error
	outcome   merge.Outcome
	mergeErr  error
	verdicts  []domain.Verdict
}

func (f *fakeMergeService) Check(context.Context, domain.Hive, int64) (merge.Readiness, error) {
	return f.readiness, f.checkErr
}

func (f *fakeMergeService) Merge(context.Context, domain.Hive, int64) (merge.Outcome, error) {
	return f.outcome, f.mergeErr
}

func (f *fakeMergeService) Verdicts(context.Context, string, int64) ([]domain.Verdict, error) {
	return f.verdicts, nil
}

func readyReadiness() merge.Readiness {
	return merge.Readiness{
		Ready:        true,
		HeadSHA:      "abcdef0123456789",
		VerdictFound: true,
		Verdict:      domain.Verdict{Result: domain.VerdictPass, HeadSHA: "abcdef0123456789"},
		Pull: merge.Pull{
			Number: 7, State: "open", HeadSHA: "abcdef0123456789",
			URL: "https://github.com/acme/widgets/pull/7",
		},
		Checks:  []merge.Check{},
		Reasons: []string{},
	}
}

func TestPRStatusReturnsChecksAndVerdicts(t *testing.T) {
	svc := &fakeMergeService{
		readiness: readyReadiness(),
		verdicts: []domain.Verdict{{
			ID: "v1", HiveID: "hive-1", PRNumber: 7, HeadSHA: "abcdef0123456789",
			Result: domain.VerdictPass, Summary: "looks good", CommentURL: "https://github.com/acme/widgets/pull/7#c1",
		}},
	}
	env := newTestEnv(t, Options{Merge: svc})
	h := env.seedHive("alpha", "acme/widgets")
	session := env.connect(env.userToken)
	out := decode[prStatusOutput](t, callTool(t, session, "pr_status", map[string]any{
		"hiveId": h.ID, "pr": 7,
	}))
	if !out.Ready || out.PRNumber != 7 || out.HeadSHA != "abcdef0123456789" {
		t.Fatalf("out = %+v", out)
	}
	if len(out.Verdicts) != 1 || out.Verdicts[0].Result != "pass" || out.Verdicts[0].CommentURL == "" {
		t.Fatalf("verdicts = %+v", out.Verdicts)
	}
}

func TestPRStatusTruncatesAndMarksCheckOutput(t *testing.T) {
	readiness := readyReadiness()
	readiness.Checks = []merge.Check{{
		Name: "test", Status: "completed", Conclusion: "success",
		Output: strings.Repeat("x", 5000),
	}}
	env := newTestEnv(t, Options{Merge: &fakeMergeService{readiness: readiness}})
	h := env.seedHive("alpha", "acme/widgets")
	session := env.connect(env.userToken)
	out := decode[prStatusOutput](t, callTool(t, session, "pr_status", map[string]any{
		"hiveId": h.ID, "pr": 7,
	}))
	if len(out.Checks) != 1 {
		t.Fatalf("checks = %+v", out.Checks)
	}
	check := out.Checks[0]
	if !check.Untrusted || !strings.Contains(check.Output, untrustedMarker) {
		t.Fatalf("output is not marked untrusted: %q", check.Output)
	}
	if !check.OutputCut || !strings.Contains(check.Output, "truncated") {
		t.Fatalf("output was not truncated: cut=%v output=%q", check.OutputCut, check.Output)
	}
	kept := strings.TrimPrefix(check.Output, untrustedMarker+"\n")
	if strings.Count(kept, "x") != maxCheckOutputRunes {
		t.Fatalf("kept %d characters, want %d", strings.Count(kept, "x"), maxCheckOutputRunes)
	}
}

func TestPRStatusAgentTokenOnlyReadsItsOwnPR(t *testing.T) {
	svc := &fakeMergeService{readiness: readyReadiness()}
	env := newTestEnv(t, Options{Merge: svc})
	h := env.seedHive("alpha", "acme/widgets")
	forager, err := env.backend.CreateForager(context.Background(), domain.Agent{
		HiveID: h.ID, Role: domain.RoleForager, PRNumber: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := env.backend.CreateAgentToken(context.Background(), forager.ID)
	if err != nil {
		t.Fatal(err)
	}
	session := env.connect(token)
	if out := decode[prStatusOutput](t, callTool(t, session, "pr_status", map[string]any{
		"hiveId": h.ID, "pr": 7,
	})); !out.Ready {
		t.Fatalf("own PR was refused: %+v", out)
	}
	res, err := callToolRaw(t, session, "pr_status", map[string]any{"hiveId": h.ID, "pr": 8})
	if !denied(t, res, err) {
		t.Fatal("agent token read another PR")
	}
}

func TestMergePRAgentTokenIsRejected(t *testing.T) {
	env := newTestEnv(t, Options{Merge: &fakeMergeService{readiness: readyReadiness()}})
	h := env.seedHive("alpha", "acme/widgets")
	forager, err := env.backend.CreateForager(context.Background(), domain.Agent{
		HiveID: h.ID, Role: domain.RoleForager, PRNumber: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := env.backend.CreateAgentToken(context.Background(), forager.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := callToolRaw(t, env.connect(token), "merge_pr", map[string]any{"hiveId": h.ID, "pr": 7})
	if !denied(t, res, err) {
		t.Fatal("agent token was allowed to merge")
	}
}

func TestMergePRQueuedAndRefused(t *testing.T) {
	hiveSvc := &fakeMergeService{
		readiness: readyReadiness(),
		outcome:   merge.Outcome{Queued: true, QueueEntryID: "entry", HeadSHA: "abcdef0123456789", Method: "squash"},
	}
	env := newTestEnv(t, Options{Merge: hiveSvc})
	h := env.seedHive("alpha", "acme/widgets")
	session := env.connect(env.userToken)
	out := decode[mergePROutput](t, callTool(t, session, "merge_pr", map[string]any{"hiveId": h.ID, "pr": 7}))
	if !out.Queued || out.Merged || out.QueueEntryID != "entry" || out.Message != "已排队" {
		t.Fatalf("out = %+v", out)
	}
	if out.HeadSHA != "abcdef0123456789" || out.Method != "squash" {
		t.Fatalf("out = %+v", out)
	}

	notReady := &fakeMergeService{mergeErr: &merge.NotReadyError{Readiness: merge.Readiness{
		HeadSHA: "abcdef0123456789",
		Reasons: []string{"latest verdict is fail"},
	}}}
	env = newTestEnv(t, Options{Merge: notReady})
	h = env.seedHive("alpha", "acme/widgets")
	session = env.connect(env.userToken)
	out = decode[mergePROutput](t, callTool(t, session, "merge_pr", map[string]any{"hiveId": h.ID, "pr": 7}))
	if out.Ready || out.Merged || len(out.Reasons) != 1 || !strings.Contains(out.Message, "未合并") {
		t.Fatalf("out = %+v", out)
	}
}

func TestMergePRReportsGitHubError(t *testing.T) {
	svc := &fakeMergeService{
		readiness: readyReadiness(),
		mergeErr:  context.DeadlineExceeded,
	}
	env := newTestEnv(t, Options{Merge: svc})
	h := env.seedHive("alpha", "acme/widgets")
	res := callTool(t, env.connect(env.userToken), "merge_pr", map[string]any{"hiveId": h.ID, "pr": 7})
	if !res.IsError {
		t.Fatalf("GitHub error was not reported: %+v", res)
	}
}
