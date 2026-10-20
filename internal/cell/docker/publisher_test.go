package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/cell"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
)

type prCall struct {
	repoID int64
	source string
	target string
	title  string
	body   string
}

type fakePulls struct {
	mu     sync.Mutex
	calls  []prCall
	result forge.MergeRequest
	err    error
}

func (p *fakePulls) CreateOrUpdatePR(_ context.Context, id int64, source, target, title, body string) (forge.MergeRequest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, prCall{repoID: id, source: source, target: target, title: title, body: body})
	if p.err != nil {
		return forge.MergeRequest{}, p.err
	}
	result := p.result
	if result.IID == 0 {
		result.IID = 42
	}
	return result, nil
}

func (p *fakePulls) snapshot() []prCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]prCall(nil), p.calls...)
}

// recordingGit records every host-side git invocation for security assertions.
type recordingGit struct {
	inner *Git
	mu    sync.Mutex
	calls []gitCall
}

type gitCall struct {
	dir  string
	env  []string
	args []string
}

func (g *recordingGit) Run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	g.mu.Lock()
	g.calls = append(g.calls, gitCall{dir: dir, env: append([]string(nil), env...), args: append([]string(nil), args...)})
	g.mu.Unlock()
	return g.inner.Run(ctx, dir, env, args...)
}

func (g *recordingGit) snapshot() []gitCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]gitCall(nil), g.calls...)
}

func publisherFixture(t *testing.T) (spec cell.Spec, dataDir, workspace, bare string) {
	t.Helper()
	bare = initBareRepo(t)
	dataDir = t.TempDir()
	spec = testSpec()
	workspace = WorkspaceDir(dataDir, spec.AgentID)
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	return spec, dataDir, workspace, bare
}

// makeCellBundle builds a trusted bundle as if it came from a Cell. The commit
// author is irrelevant here; the commit command itself is asserted separately.
func makeCellBundle(t *testing.T, bare, branch, subject string) (bundle, baseSHA string) {
	t.Helper()
	root := t.TempDir()
	cellRepo := filepath.Join(root, "cell")
	runGit(t, root, "clone", bare, cellRepo)
	runGit(t, cellRepo, "checkout", "-B", branch, "origin/main")
	if subject != "" {
		if err := os.WriteFile(filepath.Join(cellRepo, "work.txt"), []byte("work\n"), 0644); err != nil {
			t.Fatal(err)
		}
		runGit(t, cellRepo, "add", "-A")
		runGit(t, cellRepo, "commit", "-m", subject)
	}
	bundle = filepath.Join(root, "publish.bundle")
	runGit(t, cellRepo, "bundle", "create", bundle, "refs/heads/"+branch)
	baseSHA = runGit(t, cellRepo, "rev-parse", "origin/main")
	return bundle, baseSHA
}

func makeOrphanBundle(t *testing.T, branch string) string {
	t.Helper()
	root := t.TempDir()
	cellRepo := filepath.Join(root, "orphan")
	runGit(t, root, "init", "-b", branch, cellRepo)
	if err := os.WriteFile(filepath.Join(cellRepo, "orphan.txt"), []byte("orphan\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, cellRepo, "add", "-A")
	runGit(t, cellRepo, "commit", "-m", "unrelated history")
	bundle := filepath.Join(root, "publish.bundle")
	runGit(t, cellRepo, "bundle", "create", bundle, "refs/heads/"+branch)
	return bundle
}

func makeExtraRefBundle(t *testing.T, bare, branch string) string {
	t.Helper()
	root := t.TempDir()
	cellRepo := filepath.Join(root, "cell")
	runGit(t, root, "clone", bare, cellRepo)
	runGit(t, cellRepo, "checkout", "-B", branch, "origin/main")
	if err := os.WriteFile(filepath.Join(cellRepo, "work.txt"), []byte("work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, cellRepo, "add", "-A")
	runGit(t, cellRepo, "commit", "-m", "work")
	runGit(t, cellRepo, "checkout", "-B", "other", "origin/main")
	if err := os.WriteFile(filepath.Join(cellRepo, "other.txt"), []byte("other\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, cellRepo, "add", "-A")
	runGit(t, cellRepo, "commit", "-m", "other")
	bundle := filepath.Join(root, "publish.bundle")
	runGit(t, cellRepo, "bundle", "create", bundle, "refs/heads/"+branch, "refs/heads/other")
	return bundle
}

// bundleCellRunner simulates the container side. It copies a pre-built bundle
// into the host workspace, which is the only output the host is allowed to
// consume.
func bundleCellRunner(workspace, branch, bundle string, staged bool) *fakeCellRunner {
	return &fakeCellRunner{handler: func(argv []string) (Result, error) {
		if len(argv) > 0 && argv[0] == "mkdir" {
			if err := os.MkdirAll(filepath.Join(workspace, ".swarmjet"), 0700); err != nil {
				return Result{ExitCode: 1, Stderr: []byte(err.Error())}, nil
			}
			return Result{}, nil
		}
		if len(argv) < 2 || argv[0] != "git" {
			return Result{}, nil
		}
		switch argv[1] {
		case "symbolic-ref":
			return Result{Stdout: []byte(branch + "\n")}, nil
		case "diff":
			if staged {
				return Result{ExitCode: 1}, nil
			}
			return Result{}, nil
		case "bundle":
			data, err := os.ReadFile(bundle)
			if err != nil {
				return Result{ExitCode: 1, Stderr: []byte(err.Error())}, nil
			}
			dest := filepath.Join(workspace, ".swarmjet", cellBundleName)
			if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
				return Result{ExitCode: 1, Stderr: []byte(err.Error())}, nil
			}
			if err := os.WriteFile(dest, data, 0644); err != nil {
				return Result{ExitCode: 1, Stderr: []byte(err.Error())}, nil
			}
			return Result{}, nil
		}
		return Result{}, nil
	}}
}

func newTestPublisher(dataDir, bare string, cellRunner CellRunner) (*Publisher, *fakePulls) {
	pulls := &fakePulls{}
	return &Publisher{
		DataDir:        dataDir,
		Git:            &Git{Remote: func(string) string { return bare }},
		RemoteURL:      func(string) string { return bare },
		Tokens:         func(context.Context, string) (string, error) { return "secret-token", nil },
		Pulls:          pulls,
		Repository:     func(context.Context, string) (int64, error) { return 7, nil },
		Cell:           cellRunner,
		CommandTimeout: 30 * 1000 * 1000 * 1000,
	}, pulls
}

func tryGit(t *testing.T, dir string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err == nil
}

func TestPublisherCommitsWIPAndPushesOnlyAgentBranch(t *testing.T) {
	spec, dataDir, workspace, bare := publisherFixture(t)
	bundle, _ := makeCellBundle(t, bare, spec.Branch, "wip: forager "+spec.AgentID)
	cellRunner := bundleCellRunner(workspace, spec.Branch, bundle, true)
	pub, pulls := newTestPublisher(dataDir, bare, cellRunner)

	pr, err := pub.Publish(context.Background(), spec, "Forager work", "handoff body")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if pr != 42 {
		t.Fatalf("pr = %d, want 42", pr)
	}
	calls := pulls.snapshot()
	if len(calls) != 1 || calls[0].repoID != 7 || calls[0].source != spec.Branch || calls[0].target != "main" ||
		calls[0].title != "Forager work" || calls[0].body != "handoff body" {
		t.Fatalf("PR calls = %+v", calls)
	}

	subject := runGit(t, bare, "log", "-1", "--format=%s", "refs/heads/"+spec.Branch)
	if subject != "wip: forager "+spec.AgentID {
		t.Fatalf("commit subject = %q", subject)
	}
	commitCall, ok := findCellGitSubcommand(cellRunner.snapshot(), "commit")
	if !ok {
		t.Fatal("WIP commit was not run inside the Cell")
	}
	for _, want := range []string{"user.name=" + wipAuthorName, "user.email=" + wipAuthorEmail, "--no-verify", "wip: forager " + spec.AgentID} {
		if !hasArg(commitCall, want) {
			t.Errorf("commit command missing %q: %v", want, commitCall)
		}
	}
	if _, ok := tryGit(t, bare, "rev-parse", "--verify", "refs/heads/other"); ok {
		t.Fatal("a non-Agent branch was pushed")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "cells", spec.AgentID, "mirror.git")); err != nil {
		t.Fatalf("mirror missing: %v", err)
	}
}

func TestPublisherNoChangesReturnsZero(t *testing.T) {
	spec, dataDir, workspace, bare := publisherFixture(t)
	bundle, _ := makeCellBundle(t, bare, spec.Branch, "")
	cellRunner := bundleCellRunner(workspace, spec.Branch, bundle, false)
	pub, pulls := newTestPublisher(dataDir, bare, cellRunner)

	pr, err := pub.Publish(context.Background(), spec, "title", "body")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if pr != 0 {
		t.Fatalf("pr = %d, want 0", pr)
	}
	if calls := pulls.snapshot(); len(calls) != 0 {
		t.Fatalf("PR was created without commits: %+v", calls)
	}
	if _, ok := tryGit(t, bare, "rev-parse", "--verify", "refs/heads/"+spec.Branch); ok {
		t.Fatal("branch was pushed without commits")
	}
}

func TestPublisherPushesExistingCommitsWithoutWIP(t *testing.T) {
	spec, dataDir, workspace, bare := publisherFixture(t)
	bundle, _ := makeCellBundle(t, bare, spec.Branch, "real work")
	cellRunner := bundleCellRunner(workspace, spec.Branch, bundle, false)
	pub, _ := newTestPublisher(dataDir, bare, cellRunner)

	if _, err := pub.Publish(context.Background(), spec, "title", "body"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	subject := runGit(t, bare, "log", "-1", "--format=%s", "refs/heads/"+spec.Branch)
	if subject != "real work" {
		t.Fatalf("commit subject = %q, want real work", subject)
	}
	if _, ok := findCellGitSubcommand(cellRunner.snapshot(), "commit"); ok {
		t.Fatal("a WIP commit was created despite a clean workspace")
	}
}

func TestPublisherRefusesNonAgentBranch(t *testing.T) {
	spec, dataDir, workspace, bare := publisherFixture(t)
	bundle, _ := makeCellBundle(t, bare, spec.Branch, "work")
	cellRunner := bundleCellRunner(workspace, "not-"+spec.Branch, bundle, true)
	pub, pulls := newTestPublisher(dataDir, bare, cellRunner)

	if _, err := pub.Publish(context.Background(), spec, "title", "body"); err == nil {
		t.Fatal("expected a branch mismatch error")
	}
	for _, command := range cellRunner.snapshot() {
		if len(command) > 1 && command[0] == "git" && (command[1] == "add" || command[1] == "commit" || command[1] == "bundle") {
			t.Fatalf("workspace was mutated on the wrong branch: %v", command)
		}
	}
	if calls := pulls.snapshot(); len(calls) != 0 {
		t.Fatalf("PR created after branch mismatch: %+v", calls)
	}
}

func TestPublisherRefusesToPushBaseBranch(t *testing.T) {
	spec, dataDir, workspace, bare := publisherFixture(t)
	spec.Branch = "main"
	bundle, _ := makeCellBundle(t, bare, "main", "")
	cellRunner := bundleCellRunner(workspace, "main", bundle, true)
	pub, pulls := newTestPublisher(dataDir, bare, cellRunner)

	if _, err := pub.Publish(context.Background(), spec, "title", "body"); err == nil {
		t.Fatal("expected refusal to publish the base branch")
	}
	for _, command := range cellRunner.snapshot() {
		if len(command) > 1 && command[0] == "git" && (command[1] == "add" || command[1] == "commit" || command[1] == "bundle") {
			t.Fatalf("base branch workspace was mutated: %v", command)
		}
	}
	if calls := pulls.snapshot(); len(calls) != 0 {
		t.Fatalf("PR created for base branch: %+v", calls)
	}
}

func TestPublisherRejectsUnexpectedBundleRefs(t *testing.T) {
	spec, dataDir, workspace, bare := publisherFixture(t)
	bundle := makeExtraRefBundle(t, bare, spec.Branch)
	cellRunner := bundleCellRunner(workspace, spec.Branch, bundle, true)
	pub, _ := newTestPublisher(dataDir, bare, cellRunner)

	if _, err := pub.Publish(context.Background(), spec, "title", "body"); err == nil {
		t.Fatal("expected the extra bundle ref to be rejected")
	}
	if _, ok := tryGit(t, bare, "rev-parse", "--verify", "refs/heads/"+spec.Branch); ok {
		t.Fatal("branch was pushed from a bundle with unexpected refs")
	}
}

func TestPublisherRejectsNonAncestorBranch(t *testing.T) {
	spec, dataDir, workspace, bare := publisherFixture(t)
	bundle := makeOrphanBundle(t, spec.Branch)
	cellRunner := bundleCellRunner(workspace, spec.Branch, bundle, true)
	pub, _ := newTestPublisher(dataDir, bare, cellRunner)

	if _, err := pub.Publish(context.Background(), spec, "title", "body"); err == nil {
		t.Fatal("expected an ancestry error")
	}
	if _, ok := tryGit(t, bare, "rev-parse", "--verify", "refs/heads/"+spec.Branch); ok {
		t.Fatal("unrelated branch was pushed")
	}
}

func TestPublisherRejectsSymlinkBundle(t *testing.T) {
	spec, dataDir, workspace, bare := publisherFixture(t)
	bundle, _ := makeCellBundle(t, bare, spec.Branch, "work")
	dest := filepath.Join(workspace, ".swarmjet")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bundle, filepath.Join(dest, cellBundleName)); err != nil {
		t.Fatal(err)
	}
	// The simulated Cell reports success but leaves the symlink in place.
	cellRunner := &fakeCellRunner{handler: func(argv []string) (Result, error) {
		if len(argv) > 1 && argv[0] == "git" && argv[1] == "symbolic-ref" {
			return Result{Stdout: []byte(spec.Branch + "\n")}, nil
		}
		if len(argv) > 1 && argv[0] == "git" && argv[1] == "diff" {
			return Result{ExitCode: 1}, nil
		}
		return Result{}, nil
	}}
	pub, _ := newTestPublisher(dataDir, bare, cellRunner)

	if _, err := pub.Publish(context.Background(), spec, "title", "body"); err == nil {
		t.Fatal("expected the symlink bundle to be rejected")
	}
	if _, ok := tryGit(t, bare, "rev-parse", "--verify", "refs/heads/"+spec.Branch); ok {
		t.Fatal("branch was pushed from a symlinked bundle")
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestPublisherSecurityDoesNotRunWorkspaceConfigOnHost(t *testing.T) {
	spec, dataDir, workspace, bare := publisherFixture(t)
	evilBare := filepath.Join(t.TempDir(), "evil.git")
	runGit(t, filepath.Dir(evilBare), "init", "--bare", evilBare)
	marker := filepath.Join(t.TempDir(), "host-marker")
	includeFile := filepath.Join(t.TempDir(), "include.conf")
	if err := os.WriteFile(includeFile, []byte("[filter \"evil2\"]\n\tclean = !touch "+marker+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	hooksDir := t.TempDir()
	writeExecutable(t, filepath.Join(hooksDir, "pre-commit"), "#!/bin/sh\ntouch "+marker+"\n")
	writeExecutable(t, filepath.Join(hooksDir, "post-commit"), "#!/bin/sh\ntouch "+marker+"\n")
	gpgScript := filepath.Join(t.TempDir(), "evil-gpg")
	writeExecutable(t, gpgScript, "#!/bin/sh\ntouch "+marker+"\nexit 1\n")
	malicious := fmt.Sprintf(`[filter "evil"]
	clean = !touch %s
	process = !touch %s
[credential "https://github.com"]
	helper = !touch %s
[include]
	path = %s
[core]
	hooksPath = %s
[gpg]
	program = %s
[commit]
	gpgsign = true
[url "%s"]
	insteadOf = %s
`, marker, marker, marker, includeFile, hooksDir, gpgScript, evilBare, bare)
	if err := os.MkdirAll(filepath.Join(workspace, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git", "config"), []byte(malicious), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".gitattributes"), []byte("* filter=evil\n"), 0644); err != nil {
		t.Fatal(err)
	}

	bundle, _ := makeCellBundle(t, bare, spec.Branch, "wip: forager "+spec.AgentID)
	cellRunner := bundleCellRunner(workspace, spec.Branch, bundle, true)
	pub, pulls := newTestPublisher(dataDir, bare, cellRunner)
	recorder := &recordingGit{inner: &Git{Remote: func(string) string { return bare }}}
	pub.Git = recorder

	pr, err := pub.Publish(context.Background(), spec, "title", "body")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if pr != 42 {
		t.Fatalf("pr = %d, want 42", pr)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host executed workspace-controlled command: %v", err)
	}
	if _, ok := tryGit(t, bare, "rev-parse", "--verify", "refs/heads/"+spec.Branch); !ok {
		t.Fatal("expected remote did not receive the Agent branch")
	}
	if _, ok := tryGit(t, evilBare, "rev-parse", "--verify", "refs/heads/"+spec.Branch); ok {
		t.Fatal("push was redirected by a workspace insteadOf rule")
	}
	if calls := pulls.snapshot(); len(calls) != 1 {
		t.Fatalf("PR calls = %+v", calls)
	}

	absWorkspace, err := filepath.Abs(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range recorder.snapshot() {
		dir, err := filepath.Abs(call.dir)
		if err == nil {
			rel, relErr := filepath.Rel(absWorkspace, dir)
			if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				t.Fatalf("host git ran inside the Cell workspace: dir=%q args=%v", call.dir, call.args)
			}
		}
		for _, arg := range call.args {
			if strings.Contains(arg, absWorkspace) {
				t.Fatalf("host git received the workspace path in an argument: %v", call.args)
			}
			if strings.Contains(arg, "secret-token") {
				t.Fatalf("token appeared in git arguments: %v", call.args)
			}
		}
	}
	sawTokenEnv := false
	for _, call := range recorder.snapshot() {
		for _, entry := range call.env {
			if strings.Contains(entry, "secret-token") {
				sawTokenEnv = true
			}
			if strings.Contains(entry, "secret-token") && strings.Contains(entry, "GIT_ASKPASS") {
				t.Fatalf("token was written into the askpass path: %q", entry)
			}
		}
	}
	if !sawTokenEnv {
		t.Fatal("token was not passed to host git through the environment")
	}
	if strings.Contains(fmt.Sprint(err), "secret-token") {
		t.Fatalf("token leaked into the error: %v", err)
	}
}

func TestPublisherResolvesRemoteDefaultBranch(t *testing.T) {
	spec, dataDir, workspace, bare := publisherFixture(t)
	spec.BaseBranch = ""
	bundle, _ := makeCellBundle(t, bare, spec.Branch, "work")
	cellRunner := bundleCellRunner(workspace, spec.Branch, bundle, true)
	pub, pulls := newTestPublisher(dataDir, bare, cellRunner)

	if _, err := pub.Publish(context.Background(), spec, "title", "body"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	calls := pulls.snapshot()
	if len(calls) != 1 || calls[0].target != "main" {
		t.Fatalf("PR target = %+v", calls)
	}
}

func TestPublisherRedactsCellOutput(t *testing.T) {
	spec, dataDir, _, bare := publisherFixture(t)
	cellRunner := &fakeCellRunner{handler: func(argv []string) (Result, error) {
		if len(argv) > 1 && argv[0] == "git" && argv[1] == "symbolic-ref" {
			return Result{Stdout: []byte(spec.Branch + "\n")}, nil
		}
		if len(argv) > 1 && argv[0] == "git" && argv[1] == "diff" {
			return Result{ExitCode: 1}, nil
		}
		if len(argv) > 1 && argv[0] == "git" && argv[1] == "add" {
			return Result{ExitCode: 1, Stderr: []byte("leak llm-secret mcp-secret secret-token")}, nil
		}
		return Result{}, nil
	}}
	pub, _ := newTestPublisher(dataDir, bare, cellRunner)

	_, err := pub.Publish(context.Background(), spec, "title", "body")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, secret := range []string{"llm-secret", "mcp-secret", "secret-token"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret %q leaked into error: %v", secret, err)
		}
	}
}

func TestPublisherTokenSourceError(t *testing.T) {
	spec, dataDir, workspace, bare := publisherFixture(t)
	bundle, _ := makeCellBundle(t, bare, spec.Branch, "work")
	pub, _ := newTestPublisher(dataDir, bare, bundleCellRunner(workspace, spec.Branch, bundle, true))
	pub.Tokens = func(context.Context, string) (string, error) {
		return "", errors.New("credential unavailable")
	}
	if _, err := pub.Publish(context.Background(), spec, "title", "body"); err == nil {
		t.Fatal("expected credential error")
	}
}

func TestGitAuthUsesAskpassWithoutPersistingToken(t *testing.T) {
	auth, err := newGitAuth("secret-token")
	if err != nil {
		t.Fatal(err)
	}
	defer auth.Close()
	helper, ok := envValue(auth.env, "GIT_ASKPASS")
	if !ok || helper == "" {
		t.Fatalf("GIT_ASKPASS missing: %v", auth.env)
	}
	if token, _ := envValue(auth.env, "SWARMJET_GIT_TOKEN"); token != "secret-token" {
		t.Fatalf("token env = %q", token)
	}
	script, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(script), "secret-token") {
		t.Fatal("token was written to the askpass helper")
	}
	if !strings.Contains(string(script), "x-access-token") {
		t.Fatal("askpass helper does not provide a username")
	}
}

func TestValidBranchRejectsOptionInjection(t *testing.T) {
	cases := []string{"", "-main", "a..b", "a//b", "a/", "a.", "main\x00", "HEAD", "a.lock/b"}
	for _, branch := range cases {
		if err := validBranch(branch); err == nil {
			t.Errorf("validBranch(%q) = nil, want error", branch)
		}
	}
	for _, branch := range []string{"main", "swarm/acme/agent-1", "feature/thing.v2"} {
		if err := validBranch(branch); err != nil {
			t.Errorf("validBranch(%q) = %v", branch, err)
		}
	}
}

// findCellGitSubcommand finds a recorded `git ... <subcommand>` invocation.
func findCellGitSubcommand(calls [][]string, subcommand string) ([]string, bool) {
	for _, call := range calls {
		if len(call) > 0 && call[0] == "git" && hasArg(call, subcommand) {
			return call, true
		}
	}
	return nil, false
}
