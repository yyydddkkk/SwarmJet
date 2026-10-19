package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/cell/acp"
)

const testRevision = "0123456789abcdef0123456789abcdef01234567"

func TestStartChecksOutRevisionDetached(t *testing.T) {
	rt, exec, dialer, _ := testRuntime(t)
	spec := testSpec()
	spec.Revision = testRevision
	spec.Branch = "verify/agent-1"
	spec.ReadOnly = true

	cellRunner := &fakeCellRunner{}
	cellRunner.setHandler(func(argv []string) (Result, error) {
		if len(argv) > 1 && argv[0] == "git" {
			switch argv[1] {
			case "remote":
				return Result{Stdout: []byte("https://github.com/acme/app.git\n")}, nil
			case "cat-file":
				return Result{}, nil
			case "checkout":
				return Result{}, nil
			}
		}
		return Result{}, nil
	})
	rt.Cell = cellRunner
	exec.setHandler(dockerHandler(false, false))
	dialer.newClient = func() ACPClient {
		return &fakeACP{
			initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: true}},
			newResult:  acp.NewSessionResult{SessionID: "ses-revision"},
		}
	}

	if _, err := rt.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var checkouts [][]string
	for _, call := range cellRunner.snapshot() {
		if len(call) > 1 && call[0] == "git" {
			switch call[1] {
			case "checkout":
				checkouts = append(checkouts, call)
			case "symbolic-ref", "show-ref", "ls-files":
				t.Fatalf("revision checkout used branch preparation command %v", call)
			}
		}
	}
	if len(checkouts) != 2 {
		t.Fatalf("checkout calls = %d, want 2: %v", len(checkouts), checkouts)
	}
	if !hasArg(checkouts[0], "-B") || !hasArg(checkouts[0], spec.Branch) || !hasArg(checkouts[0], testRevision) {
		t.Fatalf("local branch checkout = %v", checkouts[0])
	}
	if !hasArg(checkouts[1], "--detach") || !hasArg(checkouts[1], testRevision) {
		t.Fatalf("detached checkout = %v", checkouts[1])
	}
}

func TestStartRevisionMissingReturnsClearError(t *testing.T) {
	rt, exec, dialer, _ := testRuntime(t)
	spec := testSpec()
	spec.Revision = testRevision
	spec.ReadOnly = true

	cellRunner := &fakeCellRunner{}
	cellRunner.setHandler(func(argv []string) (Result, error) {
		if len(argv) > 2 && argv[0] == "git" && argv[1] == "cat-file" {
			return Result{ExitCode: 128, Stderr: []byte("fatal: Not a valid object name")}, nil
		}
		if len(argv) > 1 && argv[0] == "git" && argv[1] == "remote" {
			return Result{Stdout: []byte("https://github.com/acme/app.git\n")}, nil
		}
		return Result{}, nil
	})
	rt.Cell = cellRunner
	exec.setHandler(dockerHandler(false, false))
	dialer.newClient = func() ACPClient {
		return &fakeACP{newResult: acp.NewSessionResult{SessionID: "ses-revision"}}
	}

	_, err := rt.Start(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "not present in the workspace") {
		t.Fatalf("Start error = %v, want a clear missing-revision error", err)
	}
	for _, call := range cellRunner.snapshot() {
		if len(call) > 1 && call[0] == "git" && call[1] == "checkout" {
			t.Fatalf("missing revision was checked out: %v", call)
		}
	}
}

func TestStartRejectsInvalidRevision(t *testing.T) {
	rt, exec, _, _ := testRuntime(t)
	for _, revision := range []string{"HEAD", "--detach", "main..other", "not-a-sha"} {
		spec := testSpec()
		spec.Revision = revision
		if _, err := rt.Start(context.Background(), spec); err == nil {
			t.Errorf("revision %q was accepted", revision)
		}
	}
	if calls := exec.snapshot(); len(calls) != 0 {
		t.Fatalf("invalid revisions reached docker: %v", calls)
	}
}

func TestPublisherRefusesReadOnlySpec(t *testing.T) {
	spec := testSpec()
	spec.ReadOnly = true
	pub := &Publisher{DataDir: t.TempDir()}
	pr, err := pub.Publish(context.Background(), spec, "title", "body")
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("Publish error = %v, want a read-only refusal", err)
	}
	if pr != 0 {
		t.Fatalf("pr = %d, want 0", pr)
	}
}

func TestValidateSpecAcceptsRevision(t *testing.T) {
	if err := validateSpec(cell.Spec{
		AgentID: "agent-1", HiveID: "hive-1", Repository: "acme/app",
		Branch: "verify/agent-1", CLI: "opencode", Model: "opencode/big-pickle",
		Revision: testRevision, ReadOnly: true,
	}, "image:latest"); err != nil {
		t.Fatalf("valid revision rejected: %v", err)
	}
}
