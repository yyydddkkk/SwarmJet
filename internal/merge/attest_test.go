package merge

import (
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func testActor() Actor {
	return Actor{ID: "verifier-1", Label: "Agent verifier-1", RoleName: "verifier", IsAgent: true}
}

func TestAttestStoresSupersededForOldHead(t *testing.T) {
	stub := readyStub()
	store := &fakeStore{hive: testHive(), owner: domain.Agent{ID: "forager-1", HiveID: "hive-1"}}
	attestations := &fakeAttestations{}
	svc := newTestService(t, stub, store, attestations, methodologyWith(requireAllPolicy()), nil)

	out, err := svc.Attest(context.Background(), AttestRequest{
		Hive: testHive(), PR: 7, SHA: "old-head",
		Result: domain.AttestPass, Summary: "looks fine",
		Actor: testActor(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Created || !out.Superseded || !out.CommentPosted {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Attestation.AuthorID != "forager-1" || out.Attestation.IsAuthor {
		t.Fatalf("attestation = %+v", out.Attestation)
	}
	if len(attestations.records) != 1 || !attestations.records[0].Superseded {
		t.Fatalf("records = %+v", attestations.records)
	}
	if len(stub.commentBodies) != 1 {
		t.Fatalf("comments = %d, want 1", len(stub.commentBodies))
	}
	body := stub.commentBodies[0]
	for _, want := range []string{"作废", "old-head", "Agent verifier-1", "是否作者：否", "looks fine"} {
		if !strings.Contains(body, want) {
			t.Fatalf("comment does not contain %q:\n%s", want, body)
		}
	}
}

func TestAttestIsIdempotentAndDoesNotDuplicateTheComment(t *testing.T) {
	stub := readyStub()
	attestations := &fakeAttestations{}
	svc := newTestService(t, stub, &fakeStore{hive: testHive(), owner: domain.Agent{ID: "forager-1"}}, attestations, methodologyWith(requireAllPolicy()), nil)
	req := AttestRequest{
		Hive: testHive(), PR: 7, SHA: "head",
		Result: domain.AttestPass, Summary: "first", Actor: testActor(),
	}
	first, err := svc.Attest(context.Background(), req)
	if err != nil || !first.Created || !first.CommentPosted {
		t.Fatalf("first = %+v %v", first, err)
	}
	req.Summary = "a retry must not rewrite history"
	second, err := svc.Attest(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.CommentPosted {
		t.Fatalf("second = %+v, want an idempotent replay", second)
	}
	if second.Attestation.ID != first.Attestation.ID || second.Attestation.Summary != "first" {
		t.Fatalf("second = %+v, want the first stored record", second.Attestation)
	}
	if len(attestations.records) != 1 {
		t.Fatalf("records = %d, want 1", len(attestations.records))
	}
	if len(stub.commentBodies) != 1 {
		t.Fatalf("comments = %d, want 1", len(stub.commentBodies))
	}
}

func TestAttestMarksTheAuthor(t *testing.T) {
	stub := readyStub()
	attestations := &fakeAttestations{}
	svc := newTestService(t, stub, &fakeStore{hive: testHive(), owner: domain.Agent{ID: "verifier-1"}}, attestations, methodologyWith(requireAllPolicy()), nil)
	out, err := svc.Attest(context.Background(), AttestRequest{
		Hive: testHive(), PR: 7, SHA: "head", Result: domain.AttestResult("work"), Summary: "self check", Actor: testActor(),
	})
	if err == nil {
		t.Fatalf("invalid result was accepted: %+v", out)
	}
	out, err = svc.Attest(context.Background(), AttestRequest{
		Hive: testHive(), PR: 7, SHA: "head", Result: domain.AttestPass, Summary: "self check", Actor: testActor(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Attestation.IsAuthor {
		t.Fatalf("author was not recorded: %+v", out.Attestation)
	}
	if !strings.Contains(stub.commentBodies[0], "是否作者：是") {
		t.Fatalf("comment = %q", stub.commentBodies[0])
	}
}

func TestAttestRequiresTheAttestPermission(t *testing.T) {
	stub := readyStub()
	attestations := &fakeAttestations{}
	svc := newTestService(t, stub, &fakeStore{hive: testHive()}, attestations, methodologyWith(requireAllPolicy()), nil)
	_, err := svc.Attest(context.Background(), AttestRequest{
		Hive: testHive(), PR: 7, SHA: "head", Result: domain.AttestPass, Summary: "nope",
		Actor: Actor{ID: "worker-1", RoleName: "worker", IsAgent: true},
	})
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want a permission denial", err)
	}
	if len(attestations.records) != 0 {
		t.Fatalf("denied attestation was stored: %+v", attestations.records)
	}
	// A user token is not subject to the role table.
	if _, err = svc.Attest(context.Background(), AttestRequest{
		Hive: testHive(), PR: 7, SHA: "head", Result: domain.AttestPass, Summary: "human",
		Actor: Actor{ID: "local:human", Label: "User token local"},
	}); err != nil {
		t.Fatalf("user attestation was refused: %v", err)
	}
}

func TestAttestClampsSummaryFindingsAndComment(t *testing.T) {
	stub := readyStub()
	attestations := &fakeAttestations{}
	svc := newTestService(t, stub, &fakeStore{hive: testHive()}, attestations, methodologyWith(requireAllPolicy()), nil)
	findings := make([]domain.Finding, 25)
	for i := range findings {
		findings[i] = domain.Finding{
			Severity: "warning",
			Location: strings.Repeat("l", 1500),
			Finding:  strings.Repeat("f", 1500),
			Evidence: strings.Repeat("e", 1500),
		}
	}
	out, err := svc.Attest(context.Background(), AttestRequest{
		Hive: testHive(), PR: 7, SHA: "head", Result: domain.AttestPassNotes,
		Summary: strings.Repeat("s", 5000), Findings: findings, Actor: testActor(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(out.Attestation.Summary)) != maxAttestationSummary {
		t.Fatalf("summary runes = %d, want %d", len([]rune(out.Attestation.Summary)), maxAttestationSummary)
	}
	if len(out.Attestation.Findings) != maxAttestationFindings {
		t.Fatalf("findings = %d, want %d", len(out.Attestation.Findings), maxAttestationFindings)
	}
	for _, finding := range out.Attestation.Findings {
		if len([]rune(finding.Location)) > maxAttestationFinding ||
			len([]rune(finding.Finding)) > maxAttestationFinding ||
			len([]rune(finding.Evidence)) > maxAttestationFinding {
			t.Fatalf("finding was not clamped: %+v", finding)
		}
	}
	body := stub.commentBodies[0]
	if !strings.Contains(body, "1. [warning]") || strings.Contains(body, "21. [warning]") {
		t.Fatalf("comment finding count is wrong:\n%s", body)
	}
	// Every rendered finding line is bounded to the contract length.
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "   evidence: ") && len([]rune(line)) > maxAttestationFinding+len("   evidence: ") {
			t.Fatalf("evidence line is too long: %d", len([]rune(line)))
		}
	}
}

func TestAttestEnqueuesOneMergeCheck(t *testing.T) {
	stub := readyStub()
	inserter := &fakeInserter{}
	svc := newTestService(t, stub, &fakeStore{hive: testHive()}, &fakeAttestations{}, methodologyWith(requireAllPolicy()), inserter)
	if _, err := svc.Attest(context.Background(), AttestRequest{
		Hive: testHive(), PR: 7, SHA: "head", Result: domain.AttestPass, Summary: "ok", Actor: testActor(),
	}); err != nil {
		t.Fatal(err)
	}
	inserted := inserter.inserted()
	if len(inserted) != 1 || inserted[0].HiveID != "hive-1" || inserted[0].PR != 7 || inserted[0].SHA != "head" {
		t.Fatalf("inserted = %+v", inserted)
	}
}

func TestAttestCommentFailureStillRecordsTheConclusion(t *testing.T) {
	stub := readyStub()
	stub.commentStatus = 500
	attestations := &fakeAttestations{}
	svc := newTestService(t, stub, &fakeStore{hive: testHive()}, attestations, methodologyWith(requireAllPolicy()), nil)
	out, err := svc.Attest(context.Background(), AttestRequest{
		Hive: testHive(), PR: 7, SHA: "head", Result: domain.AttestPass, Summary: "ok", Actor: testActor(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Created || out.CommentPosted || out.CommentError == "" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(attestations.records) != 1 {
		t.Fatalf("conclusion was not stored: %+v", attestations.records)
	}
}

func TestAttestRejectsIncompleteInput(t *testing.T) {
	svc := newTestService(t, readyStub(), &fakeStore{hive: testHive()}, &fakeAttestations{}, methodologyWith(requireAllPolicy()), nil)
	cases := []AttestRequest{
		{Hive: testHive(), PR: 0, SHA: "head", Result: domain.AttestPass, Summary: "x", Actor: testActor()},
		{Hive: testHive(), PR: 7, SHA: "", Result: domain.AttestPass, Summary: "x", Actor: testActor()},
		{Hive: testHive(), PR: 7, SHA: "head", Result: "maybe", Summary: "x", Actor: testActor()},
		{Hive: testHive(), PR: 7, SHA: "head", Result: domain.AttestPass, Summary: "  ", Actor: testActor()},
		{Hive: testHive(), PR: 7, SHA: "head", Result: domain.AttestPass, Summary: "x"},
	}
	for i, req := range cases {
		if _, err := svc.Attest(context.Background(), req); err == nil {
			t.Fatalf("case %d was accepted", i)
		}
	}
}
