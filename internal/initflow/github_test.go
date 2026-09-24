package initflow

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"swarmjet/internal/domain"
	"swarmjet/internal/execution"
	"swarmjet/internal/forge"
	"testing"
)

func TestGitHubInitializationAskpass(t *testing.T) {
	helper, err := filepath.Abs("../../scripts/askpass.sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMJET_GIT_ASKPASS", helper)
	m := &Manager{DataDir: t.TempDir(), CredentialFor: func(p domain.Project) (string, error) {
		if p.HostingProvider() != domain.ProviderGitHub {
			t.Fatal("provider lost")
		}
		return "fresh-installation-token", nil
	}, AnalysisEnv: map[string]string{"GITHUB_TOKEN": "ambient", "GITLAB_TOKEN": "ambient"}}
	cmd := exec.Command("git", "-c", "credential.helper=", "credential", "fill")
	cmd.Env = m.gitEnv(domain.Project{ID: "p", Provider: domain.ProviderGitHub})
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "username=x-access-token") || !strings.Contains(string(out), "password=fresh-installation-token") {
		t.Fatal("incorrect installation authentication")
	}
}

func TestGitHubInitializationUsesForgeAndRejectsPublication(t *testing.T) {
	f := newFixture(t)
	f.project.Provider = domain.ProviderGitHub
	f.project.GitLabURL = ""
	f.repo.projects[f.project.ID] = f.project
	called := false
	f.m.ForgeClients = func(p domain.Project) (forge.Client, error) { called = true; return f.clients(p) }
	in := domain.Initialization{ID: InitID(f.project.ID), ProjectID: f.project.ID, Status: domain.InitRunning}
	out, err := f.m.connect(context.Background(), in, f.project)
	if err != nil || !called || !out.RepositoriesConnected {
		t.Fatalf("forge connect failed: %+v %v", out, err)
	}
	out.Status = domain.InitReady
	f.repo.inits[out.ID] = out
	_, err = f.m.QueuePublication(context.Background(), f.project.ID, PublishRequest{})
	if err == nil || !strings.Contains(err.Error(), "GitHub") {
		t.Fatalf("expected clear unsupported publication error: %v", err)
	}
}

func TestGitHubAnalysisDoesNotReceiveHostingCredentials(t *testing.T) {
	f := newFixture(t)
	f.project.Provider = domain.ProviderGitHub
	f.m.AnalysisEnv = map[string]string{"GITHUB_TOKEN": "secret", "GH_TOKEN": "secret", "GITHUB_APP_PRIVATE_KEY": "secret", "HTTPS_PROXY": "http://proxy"}
	f.agents.onStart = func(req domain.AgentRequest) (domain.Execution, error) {
		return domain.Execution{ID: req.ExecutionID, Status: "running"}, nil
	}
	in := domain.Initialization{ID: InitID(f.project.ID), ProjectID: f.project.ID, ExecutionID: "init-p1-1", RepositorySHAs: map[string]string{"svc": "sha-dev"}}
	_, err := AnalyzeAtNode(context.Background(), f.m.DataDir, f.agents, execution.AnalysisParams{Project: f.project, Initialization: in}, f.m)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.agents.requests) != 1 {
		t.Fatal("agent not started")
	}
	env := f.agents.requests[0].Env
	if len(env) != 1 || env["HTTPS_PROXY"] != "http://proxy" {
		t.Fatal("unexpected analysis environment")
	}
}
