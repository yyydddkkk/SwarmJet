package docker

import (
	"context"
	"errors"
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

func newPublisher(dataDir, bare string, pulls *fakePulls) *Publisher {
	return &Publisher{
		DataDir: dataDir,
		Git:     &Git{Remote: func(string) string { return bare }},
		Tokens:  func(context.Context, string) (string, error) { return "secret-token", nil },
		Pulls:   pulls,
		Repository: func(context.Context, string) (int64, error) {
			return 7, nil
		},
	}
}

func prepareWorkspace(t *testing.T, dataDir, bare string) (cell.Spec, string) {
	t.Helper()
	spec := testSpec()
	dir := WorkspaceDir(dataDir, spec.AgentID)
	gitWorkspace := &GitWorkspace{Git: &Git{Remote: func(string) string { return bare }}}
	if err := gitWorkspace.Prepare(context.Background(), spec, dir); err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	return spec, dir
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

func TestPublisherNoChangesReturnsZero(t *testing.T) {
	bare := initBareRepo(t)
	dataDir := t.TempDir()
	spec, _ := prepareWorkspace(t, dataDir, bare)
	pulls := &fakePulls{}
	pub := newPublisher(dataDir, bare, pulls)

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

func TestPublisherCommitsWIPAndPushesOnlyAgentBranch(t *testing.T) {
	bare := initBareRepo(t)
	dataDir := t.TempDir()
	spec, dir := prepareWorkspace(t, dataDir, bare)

	runGit(t, dir, "checkout", "-b", "other")
	if err := os.WriteFile(filepath.Join(dir, "other.txt"), []byte("other\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "other branch only")
	runGit(t, dir, "checkout", spec.Branch)

	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("work\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pulls := &fakePulls{}
	pub := newPublisher(dataDir, bare, pulls)

	pr, err := pub.Publish(context.Background(), spec, "Forager work", "handoff body")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if pr != 42 {
		t.Fatalf("pr = %d, want 42", pr)
	}
	calls := pulls.snapshot()
	if len(calls) != 1 {
		t.Fatalf("PR calls = %+v", calls)
	}
	if calls[0].repoID != 7 || calls[0].source != spec.Branch || calls[0].target != "main" || calls[0].title != "Forager work" || calls[0].body != "handoff body" {
		t.Fatalf("PR call = %+v", calls[0])
	}

	subject := runGit(t, bare, "--git-dir", bare, "log", "-1", "--format=%s", "refs/heads/"+spec.Branch)
	if subject != "wip: forager "+spec.AgentID {
		t.Fatalf("commit subject = %q", subject)
	}
	author := runGit(t, bare, "--git-dir", bare, "log", "-1", "--format=%an", "refs/heads/"+spec.Branch)
	if author != wipAuthorName {
		t.Fatalf("commit author = %q", author)
	}
	if _, ok := tryGit(t, bare, "--git-dir", bare, "rev-parse", "--verify", "refs/heads/other"); ok {
		t.Fatal("a non-Agent branch was pushed")
	}
	remote := runGit(t, dir, "remote", "get-url", "origin")
	if remote != bare || strings.Contains(remote, "secret-token") {
		t.Fatalf("remote URL = %q", remote)
	}
	if status := runGit(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("workspace still dirty: %q", status)
	}
}

func TestPublisherPushesExistingCommitsWithoutWIP(t *testing.T) {
	bare := initBareRepo(t)
	dataDir := t.TempDir()
	spec, dir := prepareWorkspace(t, dataDir, bare)

	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-m", "real work")

	pulls := &fakePulls{}
	pub := newPublisher(dataDir, bare, pulls)
	if _, err := pub.Publish(context.Background(), spec, "title", "body"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	subject := runGit(t, bare, "--git-dir", bare, "log", "-1", "--format=%s", "refs/heads/"+spec.Branch)
	if subject != "real work" {
		t.Fatalf("commit subject = %q, want real work", subject)
	}
}

func TestPublisherRefusesNonAgentBranch(t *testing.T) {
	bare := initBareRepo(t)
	dataDir := t.TempDir()
	spec, dir := prepareWorkspace(t, dataDir, bare)
	runGit(t, dir, "checkout", "main")
	if err := os.WriteFile(filepath.Join(dir, "wrong-branch.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}

	pulls := &fakePulls{}
	pub := newPublisher(dataDir, bare, pulls)
	if _, err := pub.Publish(context.Background(), spec, "title", "body"); err == nil {
		t.Fatal("expected a branch mismatch error")
	}
	if subject := runGit(t, dir, "log", "-1", "--format=%s"); subject != "initial" {
		t.Fatalf("wrong-branch work was committed: %q", subject)
	}
	if status := runGit(t, dir, "status", "--porcelain"); status == "" {
		t.Fatal("branch mismatch must not stage or commit workspace changes")
	}
	if calls := pulls.snapshot(); len(calls) != 0 {
		t.Fatalf("PR created after branch mismatch: %+v", calls)
	}
	if _, ok := tryGit(t, bare, "--git-dir", bare, "rev-parse", "--verify", "refs/heads/"+spec.Branch); ok {
		t.Fatal("branch was pushed after branch mismatch")
	}
}

func TestPublisherRefusesToPushBaseBranch(t *testing.T) {
	bare := initBareRepo(t)
	dataDir := t.TempDir()
	spec := testSpec()
	spec.Branch = "main"
	dir := WorkspaceDir(dataDir, spec.AgentID)
	gitWorkspace := &GitWorkspace{Git: &Git{Remote: func(string) string { return bare }}}
	if err := gitWorkspace.Prepare(context.Background(), spec, dir); err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "change.txt"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	pulls := &fakePulls{}
	pub := newPublisher(dataDir, bare, pulls)
	if _, err := pub.Publish(context.Background(), spec, "title", "body"); err == nil {
		t.Fatal("expected refusal to publish the base branch")
	}
	if calls := pulls.snapshot(); len(calls) != 0 {
		t.Fatalf("PR created for base branch: %+v", calls)
	}
}

func TestPublisherTokenSourceError(t *testing.T) {
	bare := initBareRepo(t)
	dataDir := t.TempDir()
	spec, _ := prepareWorkspace(t, dataDir, bare)
	pub := newPublisher(dataDir, bare, &fakePulls{})
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
	cases := []string{"", "-main", "a..b", "a//b", "a/", "a.", "main\x00", "HEAD"}
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
