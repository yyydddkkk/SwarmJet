package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/cell/acp"
	"github.com/yyydddkkk/SwarmJet/internal/cell/omp"
)

func TestBuildConfigTarUsesManagedPathsAndReadOnlyModes(t *testing.T) {
	payload, err := buildConfigTar(map[string]string{
		"methodology/config.yml": "tools:\n  approvalMode: yolo\n",
		"methodology/ROLE.md":    "role",
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]struct {
		content string
		mode    int64
	}{}
	tr := tar.NewReader(bytes.NewReader(payload))
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = struct {
			content string
			mode    int64
		}{content: string(body), mode: header.Mode}
	}
	config, ok := entries["methodology/config.yml"]
	if !ok || !strings.Contains(config.content, "approvalMode: yolo") {
		t.Fatalf("config entry = %+v", config)
	}
	role, ok := entries["methodology/ROLE.md"]
	if !ok || role.content != "role" {
		t.Fatalf("role entry = %+v", role)
	}
	for name, entry := range entries {
		if entry.mode != 0444 {
			t.Errorf("%s mode = %#o, want 0444", name, entry.mode)
		}
	}
	manifest := entries[omp.ManifestName].content
	if !strings.Contains(manifest, "methodology/config.yml\n") || !strings.Contains(manifest, "methodology/ROLE.md\n") {
		t.Fatalf("manifest = %q", manifest)
	}
}

func TestBuildConfigTarRejectsEscapingPaths(t *testing.T) {
	for _, path := range []string{"../escape", "/absolute", "a/../../b"} {
		if _, err := buildConfigTar(map[string]string{path: "x"}); err == nil {
			t.Errorf("path %q was accepted", path)
		}
	}
}

func TestStartFailsWhenConfigWriteFails(t *testing.T) {
	rt, exec, dialer, _ := testRuntime(t)
	writer := &fakeConfigWriter{err: errors.New("no volume")}
	rt.ConfigWriter = writer
	dialer.newClient = func() ACPClient {
		return &fakeACP{newResult: acp.NewSessionResult{SessionID: "ses-new"}}
	}
	exec.setHandler(dockerHandler(false, false))
	_, err := rt.Start(context.Background(), testSpec())
	if err == nil || !strings.Contains(err.Error(), "write omp configuration") {
		t.Fatalf("Start error = %v", err)
	}
	if len(dialer.records()) != 0 {
		t.Fatal("CLI was started after the configuration write failed")
	}
}

func TestStartInstallsRolePromptArgument(t *testing.T) {
	rt, exec, dialer, _ := testRuntime(t)
	spec := testSpec()
	spec.ConfigFiles["methodology/ROLE.md"] = "verify only"
	dialer.newClient = func() ACPClient {
		return &fakeACP{newResult: acp.NewSessionResult{SessionID: "ses-new"}}
	}
	exec.setHandler(dockerHandler(false, false))
	if _, err := rt.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	record := dialer.records()[0]
	if !hasArg(record.argv, "--append-system-prompt") || !hasArg(record.argv, omp.RolePromptPath) {
		t.Fatalf("argv = %v", record.argv)
	}
}

func TestStartDoesNotWriteConfigIntoTheWorkspace(t *testing.T) {
	rt, exec, dialer, _ := testRuntime(t)
	dialer.newClient = func() ACPClient {
		return &fakeACP{newResult: acp.NewSessionResult{SessionID: "ses-new"}}
	}
	exec.setHandler(dockerHandler(false, false))
	if _, err := rt.Start(context.Background(), testSpec()); err != nil {
		t.Fatal(err)
	}
	writer := rt.ConfigWriter.(*fakeConfigWriter)
	if len(writer.container) != 1 || writer.container[0] != containerName(testSpec().AgentID) {
		t.Fatalf("config writer container = %v", writer.container)
	}
	// Config files are addressed below the Cell Home volume, never /workspace.
	for _, files := range writer.snapshot() {
		for key := range files {
			if strings.HasPrefix(key, "workspace/") || strings.HasPrefix(key, "/workspace") {
				t.Fatalf("config path escaped into the workspace: %q", key)
			}
			if !strings.HasPrefix(key, omp.ConfigPrefix) {
				t.Fatalf("config path %q is outside the omp agent directory", key)
			}
		}
	}
}

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
		return &fakeACP{newResult: acp.NewSessionResult{SessionID: "ses-revision"}}
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
	rt, exec, _, _ := testRuntime(t)
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

const testRevision = "0123456789abcdef0123456789abcdef01234567"
