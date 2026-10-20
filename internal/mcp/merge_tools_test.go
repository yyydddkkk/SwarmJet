package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
)

type fakeMergeService struct {
	readiness     merge.Readiness
	checkErr      error
	outcome       merge.Outcome
	mergeErr      error
	attestations  []domain.Attestation
	attestOutcome merge.AttestOutcome
	attestErr     error
	permissions   map[string]domain.RolePermissions
	permissionErr error
	lastAttest    merge.AttestRequest
}

func (f *fakeMergeService) Check(context.Context, domain.Hive, int64) (merge.Readiness, error) {
	return f.readiness, f.checkErr
}

func (f *fakeMergeService) Merge(context.Context, domain.Hive, int64) (merge.Outcome, error) {
	return f.outcome, f.mergeErr
}

func (f *fakeMergeService) Attestations(context.Context, string, int64) ([]domain.Attestation, error) {
	return f.attestations, nil
}

func (f *fakeMergeService) Attest(_ context.Context, req merge.AttestRequest) (merge.AttestOutcome, error) {
	f.lastAttest = req
	return f.attestOutcome, f.attestErr
}

func (f *fakeMergeService) Permissions(_ context.Context, _, roleName string) (domain.RolePermissions, error) {
	if f.permissionErr != nil {
		return domain.RolePermissions{}, f.permissionErr
	}
	if roleName == "" {
		roleName = "worker"
	}
	if permissions, ok := f.permissions[roleName]; ok {
		return permissions, nil
	}
	return domain.RolePermissions{}, nil
}

func mcpReadyReadiness() merge.Readiness {
	return merge.Readiness{
		Ready:   true,
		HeadSHA: "abcdef0123456789",
		Pull: merge.Pull{
			Number: 7, State: "open", HeadSHA: "abcdef0123456789",
			URL: "https://github.com/acme/widgets/pull/7",
		},
		Checks:  []merge.Check{},
		Reasons: []string{},
	}
}

func TestPRStatusReturnsChecksAttestationsAndReadiness(t *testing.T) {
	svc := &fakeMergeService{
		readiness: mcpReadyReadiness(),
		attestations: []domain.Attestation{{
			ID: "att-1", HiveID: "hive-1", PRNumber: 7, HeadSHA: "abcdef0123456789",
			AttesterID: "verifier-1", AuthorID: "forager-1", Result: domain.AttestPass,
			Summary: "looks good", CommentURL: "https://github.com/acme/widgets/pull/7#c1",
		}},
	}
	env := newTestEnv(t, Options{Merge: svc})
	hive := env.seedHive("alpha", "acme/widgets")
	out := decode[prStatusOutput](t, callTool(t, env.connect(env.userToken), "pr_status", map[string]any{
		"hiveId": hive.ID, "pr": 7,
	}))
	if !out.Ready || out.PRNumber != 7 || out.HeadSHA != "abcdef0123456789" {
		t.Fatalf("out = %+v", out)
	}
	if len(out.Attestations) != 1 || out.Attestations[0].Result != "pass" || out.Attestations[0].CommentURL == "" {
		t.Fatalf("attestations = %+v", out.Attestations)
	}
}

func TestPRStatusTruncatesAndMarksCheckOutput(t *testing.T) {
	readiness := mcpReadyReadiness()
	readiness.Checks = []merge.Check{{
		Name: "test", Status: "completed", Conclusion: "success",
		Output: strings.Repeat("x", 5000),
	}}
	finding := domain.Finding{
		Severity: "warning",
		Location: strings.Repeat("l", 3000),
		Finding:  strings.Repeat("f", 3000),
		Evidence: strings.Repeat("e", 3000),
	}
	attestations := []domain.Attestation{{
		ID: "att-1", HiveID: "hive-1", PRNumber: 7, HeadSHA: "abcdef0123456789",
		Result: domain.AttestPassNotes, Summary: strings.Repeat("s", 5000),
		Findings: make([]domain.Finding, 25),
	}}
	for i := range attestations[0].Findings {
		attestations[0].Findings[i] = finding
	}
	svc := &fakeMergeService{readiness: readiness, attestations: attestations}
	env := newTestEnv(t, Options{Merge: svc})
	hive := env.seedHive("alpha", "acme/widgets")
	out := decode[prStatusOutput](t, callTool(t, env.connect(env.userToken), "pr_status", map[string]any{
		"hiveId": hive.ID, "pr": 7,
	}))
	if len(out.Checks) != 1 {
		t.Fatalf("checks = %+v", out.Checks)
	}
	check := out.Checks[0]
	if !check.Untrusted || !strings.Contains(check.Output, untrustedMarker) || !check.OutputCut {
		t.Fatalf("output was not marked/truncated: %+v", check)
	}
	keptCheck := strings.TrimPrefix(check.Output, untrustedMarker+"\n")
	keptCheck = strings.SplitN(keptCheck, "\n…[truncated", 2)[0]
	if got := strings.Count(keptCheck, "x"); got != maxCheckOutputRunes {
		t.Fatalf("kept %d check characters, want %d", got, maxCheckOutputRunes)
	}
	attestation := out.Attestations[0]
	keptSummary := strings.SplitN(attestation.Summary, "\n…[truncated", 2)[0]
	if !strings.Contains(attestation.Summary, "truncated") || strings.Count(keptSummary, "s") != maxStatusText {
		t.Fatalf("summary was not truncated: %d chars", len(attestation.Summary))
	}
	if len(attestation.Findings) != maxStatusItems {
		t.Fatalf("findings = %d, want %d", len(attestation.Findings), maxStatusItems)
	}
	for _, finding := range attestation.Findings {
		keptLocation := strings.SplitN(finding.Location, "\n…[truncated", 2)[0]
		if strings.Count(keptLocation, "l") != maxFindingText || !strings.Contains(finding.Location, "truncated") {
			t.Fatalf("finding location was not truncated: %d", len(finding.Location))
		}
	}
}

func TestPRStatusAgentTokenIsConfinedToItsHive(t *testing.T) {
	svc := &fakeMergeService{readiness: mcpReadyReadiness()}
	env := newTestEnv(t, Options{Merge: svc})
	alpha := env.seedHive("alpha", "acme/alpha")
	beta := env.seedHive("beta", "acme/beta")
	forager, err := env.backend.CreateForager(context.Background(), domain.Agent{HiveID: alpha.ID})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := env.backend.CreateAgentToken(context.Background(), forager.ID)
	if err != nil {
		t.Fatal(err)
	}
	session := env.connect(token)
	out := decode[prStatusOutput](t, callTool(t, session, "pr_status", map[string]any{"hiveId": alpha.ID, "pr": 7}))
	if !out.Ready {
		t.Fatalf("own Hive was refused: %+v", out)
	}
	res, err := callToolRaw(t, session, "pr_status", map[string]any{"hiveId": beta.ID, "pr": 7})
	if !denied(t, res, err) {
		t.Fatal("agent token read another Hive's PR")
	}
}

func TestAttestAgentTokenRequiresTheRolePermission(t *testing.T) {
	svc := &fakeMergeService{
		permissions: map[string]domain.RolePermissions{
			"worker":   {},
			"verifier": {Attest: true},
		},
		attestOutcome: merge.AttestOutcome{
			Attestation:   domain.Attestation{ID: "att-1", Result: domain.AttestPass},
			Created:       true,
			CommentPosted: true,
		},
	}
	env := newTestEnv(t, Options{Merge: svc})
	hive := env.seedHive("alpha", "acme/widgets")
	worker, err := env.backend.CreateForager(context.Background(), domain.Agent{HiveID: hive.ID, RoleName: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	workerToken, _, err := env.backend.CreateAgentToken(context.Background(), worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := callToolRaw(t, env.connect(workerToken), "attest", map[string]any{
		"hiveId": hive.ID, "pr": 7, "sha": "head", "result": "pass", "summary": "nope",
	})
	if !denied(t, res, err) {
		t.Fatal("worker without attest permission was allowed")
	}

	verifier, err := env.backend.CreateForager(context.Background(), domain.Agent{HiveID: hive.ID, RoleName: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	verifierToken, _, err := env.backend.CreateAgentToken(context.Background(), verifier.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := decode[attestOutput](t, callTool(t, env.connect(verifierToken), "attest", map[string]any{
		"hiveId": hive.ID, "pr": 7, "sha": "head", "result": "pass", "summary": "ok",
		"findings": []map[string]any{{"severity": "warning", "finding": "nit"}},
	}))
	if !out.Created || !out.CommentPosted || out.Message == "" {
		t.Fatalf("out = %+v", out)
	}
	if !svc.lastAttest.Actor.IsAgent || svc.lastAttest.Actor.ID != verifier.ID || svc.lastAttest.Actor.RoleName != "verifier" {
		t.Fatalf("actor = %+v", svc.lastAttest.Actor)
	}
	if string(svc.lastAttest.Result) != "pass" || len(svc.lastAttest.Findings) != 1 {
		t.Fatalf("request = %+v", svc.lastAttest)
	}
}

func TestMergePRAgentTokenRequiresTheRolePermission(t *testing.T) {
	svc := &fakeMergeService{
		permissions: map[string]domain.RolePermissions{
			"worker": {},
			"merger": {Merge: true},
		},
		outcome: merge.Outcome{Merged: true, HeadSHA: "head", Method: "squash"},
	}
	env := newTestEnv(t, Options{Merge: svc})
	hive := env.seedHive("alpha", "acme/widgets")
	worker, err := env.backend.CreateForager(context.Background(), domain.Agent{HiveID: hive.ID, RoleName: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	workerToken, _, err := env.backend.CreateAgentToken(context.Background(), worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := callToolRaw(t, env.connect(workerToken), "merge_pr", map[string]any{"hiveId": hive.ID, "pr": 7})
	if !denied(t, res, err) {
		t.Fatal("worker without merge permission was allowed")
	}

	merger, err := env.backend.CreateForager(context.Background(), domain.Agent{HiveID: hive.ID, RoleName: "merger"})
	if err != nil {
		t.Fatal(err)
	}
	mergerToken, _, err := env.backend.CreateAgentToken(context.Background(), merger.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := decode[mergePROutput](t, callTool(t, env.connect(mergerToken), "merge_pr", map[string]any{"hiveId": hive.ID, "pr": 7}))
	if !out.Merged || out.Message != "已合并" {
		t.Fatalf("out = %+v", out)
	}
}

func TestMergePRQueuedAndNotReady(t *testing.T) {
	svc := &fakeMergeService{
		outcome: merge.Outcome{Queued: true, QueueEntryID: "entry", HeadSHA: "abcdef0123456789", Method: "squash"},
	}
	env := newTestEnv(t, Options{Merge: svc})
	hive := env.seedHive("alpha", "acme/widgets")
	out := decode[mergePROutput](t, callTool(t, env.connect(env.userToken), "merge_pr", map[string]any{"hiveId": hive.ID, "pr": 7}))
	if !out.Queued || out.Merged || out.QueueEntryID != "entry" || out.Message != "已排队" {
		t.Fatalf("out = %+v", out)
	}

	notReady := &fakeMergeService{mergeErr: &merge.NotReadyError{Readiness: merge.Readiness{
		HeadSHA: "abcdef0123456789",
		Reasons: []string{"latest attestation is fail"},
	}}}
	env = newTestEnv(t, Options{Merge: notReady})
	hive = env.seedHive("alpha", "acme/widgets")
	out = decode[mergePROutput](t, callTool(t, env.connect(env.userToken), "merge_pr", map[string]any{"hiveId": hive.ID, "pr": 7}))
	if out.Ready || out.Merged || len(out.Reasons) != 1 || !strings.Contains(out.Message, "未合并") {
		t.Fatalf("out = %+v", out)
	}
}

func TestAttestSupersededMessage(t *testing.T) {
	svc := &fakeMergeService{attestOutcome: merge.AttestOutcome{
		Attestation: domain.Attestation{ID: "att-1", Result: domain.AttestPass, Superseded: true},
		Created:     true,
		Superseded:  true,
	}}
	env := newTestEnv(t, Options{Merge: svc})
	hive := env.seedHive("alpha", "acme/widgets")
	out := decode[attestOutput](t, callTool(t, env.connect(env.userToken), "attest", map[string]any{
		"hiveId": hive.ID, "pr": 7, "sha": "old", "result": "pass", "summary": "stale",
	}))
	if !out.Superseded || !strings.Contains(out.Message, "作废") {
		t.Fatalf("out = %+v", out)
	}
}
