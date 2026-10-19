package mcp

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func toolNames(t *testing.T, session *mcpsdk.ClientSession) map[string]bool {
	t.Helper()
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	out := map[string]bool{}
	for _, tool := range res.Tools {
		out[tool.Name] = true
	}
	return out
}

func TestSubmitVerdictIsVerifierOnlyAndReportsStale(t *testing.T) {
	stale := false
	var calls []domain.Agent
	opts := Options{
		SubmitVerdict: func(_ context.Context, verifier domain.Agent, result domain.VerdictResult, summary string, findings []domain.Finding) (domain.Verdict, bool, error) {
			calls = append(calls, verifier)
			return domain.Verdict{
				ID:         "verdict-1",
				HiveID:     verifier.HiveID,
				PRNumber:   verifier.PRNumber,
				HeadSHA:    verifier.VerifySHA,
				OwnerID:    verifier.OwnerID,
				VerifierID: verifier.ID,
				Result:     result,
				Summary:    summary,
				Findings:   findings,
			}, stale, nil
		},
	}
	env := newTestEnv(t, opts)
	ctx := context.Background()
	hive := env.seedHive("alpha", "acme/alpha")
	worker, err := env.backend.CreateForager(ctx, domain.Agent{HiveID: hive.ID, Kind: domain.KindWorker, Task: "fix it"})
	if err != nil {
		t.Fatalf("CreateForager(worker): %v", err)
	}
	verifier, err := env.backend.CreateForager(ctx, domain.Agent{
		HiveID: hive.ID, Kind: domain.KindVerifier, PRNumber: 7, VerifySHA: "abc123", OwnerID: worker.ID,
	})
	if err != nil {
		t.Fatalf("CreateForager(verifier): %v", err)
	}
	workerToken, _, err := env.backend.CreateAgentToken(ctx, worker.ID)
	if err != nil {
		t.Fatalf("CreateAgentToken(worker): %v", err)
	}
	verifierToken, _, err := env.backend.CreateAgentToken(ctx, verifier.ID)
	if err != nil {
		t.Fatalf("CreateAgentToken(verifier): %v", err)
	}

	args := map[string]any{"result": "fail", "summary": "retry helper still uses the wall clock"}

	local := env.connect(env.userToken)
	if toolNames(t, local)["submit_verdict"] {
		t.Fatal("a local token can see submit_verdict")
	}
	if res, err := callToolRaw(t, local, "submit_verdict", args); !denied(t, res, err) {
		t.Fatal("a local token was allowed to submit a verdict")
	}

	workerSession := env.connect(workerToken)
	if toolNames(t, workerSession)["submit_verdict"] {
		t.Fatal("a worker Forager can see submit_verdict")
	}
	if res, err := callToolRaw(t, workerSession, "submit_verdict", args); !denied(t, res, err) {
		t.Fatal("a worker Forager was allowed to submit a verdict")
	}

	verifierSession := env.connect(verifierToken)
	if !toolNames(t, verifierSession)["submit_verdict"] {
		t.Fatal("a verifier Forager cannot see submit_verdict")
	}
	res, err := callToolRaw(t, verifierSession, "submit_verdict", args)
	if err != nil || res == nil || res.IsError {
		t.Fatalf("verifier submit_verdict failed: err=%v result=%+v", err, res)
	}
	out := decode[submitVerdictOutput](t, res)
	if out.Superseded || out.Verdict.VerifierID != verifier.ID || out.Verdict.PRNumber != 7 {
		t.Fatalf("output = %+v, want the verifier's fresh verdict", out)
	}
	if !strings.Contains(out.Message, "结论已记录") {
		t.Fatalf("message = %q", out.Message)
	}
	if len(calls) != 1 || calls[0].ID != verifier.ID || calls[0].Kind != domain.KindVerifier {
		t.Fatalf("SubmitVerdict calls = %+v, want the verifier agent", calls)
	}

	// The scheduler reports a stale SHA; the tool must surface that wording.
	stale = true
	res, err = callToolRaw(t, verifierSession, "submit_verdict", args)
	if err != nil || res == nil || res.IsError {
		t.Fatalf("stale submit_verdict failed: err=%v result=%+v", err, res)
	}
	out = decode[submitVerdictOutput](t, res)
	if !out.Superseded || !strings.Contains(out.Message, "该提交已被新的推送取代") {
		t.Fatalf("stale output = %+v, want the superseded message", out)
	}

	// Invalid input is rejected before the scheduler sees it.
	if res, err := callToolRaw(t, verifierSession, "submit_verdict", map[string]any{"result": "nope", "summary": "x"}); !denied(t, res, err) {
		t.Fatal("an invalid result was accepted")
	}
	if len(calls) != 2 {
		t.Fatalf("invalid input reached the scheduler: %d calls", len(calls))
	}
}

func TestSubmitVerdictWithoutSchedulerFailsClearly(t *testing.T) {
	env := newTestEnv(t, Options{})
	ctx := context.Background()
	hive := env.seedHive("alpha", "acme/alpha")
	verifier, err := env.backend.CreateForager(ctx, domain.Agent{
		HiveID: hive.ID, Kind: domain.KindVerifier, PRNumber: 7, VerifySHA: "abc123",
	})
	if err != nil {
		t.Fatalf("CreateForager: %v", err)
	}
	token, _, err := env.backend.CreateAgentToken(ctx, verifier.ID)
	if err != nil {
		t.Fatalf("CreateAgentToken: %v", err)
	}
	session := env.connect(token)
	res, err := callToolRaw(t, session, "submit_verdict", map[string]any{"result": "pass", "summary": "ok"})
	if !denied(t, res, err) || !strings.Contains(toolErrorText(res), "not configured") {
		t.Fatalf("missing scheduler: err=%v result=%+v text=%q", err, res, toolErrorText(res))
	}
}
