package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarmjet/internal/credentials"
	"swarmjet/internal/domain"
)

type fakeInitializer struct{ ready bool }

func (f *fakeInitializer) Ensure(context.Context, string) (domain.Initialization, error) {
	return domain.Initialization{}, nil
}
func (f *fakeInitializer) Get(context.Context, string) (domain.Initialization, error) {
	return domain.Initialization{}, nil
}
func (f *fakeInitializer) Retry(context.Context, string) error  { return nil }
func (f *fakeInitializer) Cancel(context.Context, string) error { return nil }
func (f *fakeInitializer) Answer(context.Context, string, map[string]string) (domain.Initialization, error) {
	return domain.Initialization{}, nil
}
func (f *fakeInitializer) ReadyForTasks(context.Context, string) bool { return f.ready }

func TestProjectCredentialBindsSavedConnectionToAddress(t *testing.T) {
	box := newConnectionBox(t)
	sealed, err := box.Seal(mustJSON(t, credentials.Payload{Token: "glpat-bound"}))
	if err != nil {
		t.Fatal(err)
	}
	conns := newFakeConnections()
	conns.items["c1"] = domain.Connection{ID: "c1", GitLabURL: "https://gitlab-a.example", Version: 1}
	conns.secrets["c1"] = sealed
	a := &server{cfg: Config{Box: box}, conns: conns}
	matching := domain.Project{ConnectionID: "c1", GitLabURL: "https://gitlab-a.example"}
	if token, err := a.projectCredential(matching); err != nil || token != "glpat-bound" {
		t.Fatalf("matching address rejected: %q %v", token, err)
	}
	mismatched := domain.Project{ConnectionID: "c1", GitLabURL: "https://gitlab-b.example"}
	if _, err := a.projectCredential(mismatched); err == nil {
		t.Fatal("saved token was resolved for a different GitLab address")
	}
	if _, err := a.projectCredential(domain.Project{ConnectionID: "c1"}); err == nil {
		t.Fatal("saved token was resolved without a project GitLab address")
	}
}

func TestSaveRejectsConnectionAndProjectAddressMismatch(t *testing.T) {
	conns := newFakeConnections()
	conns.items["c1"] = domain.Connection{ID: "c1", GitLabURL: "https://gitlab-a.example", Version: 1}
	a := &server{conns: conns}
	p := managedEnabledProject()
	p.ConnectionID = "c1"
	p.GitLabURL = "https://gitlab-b.example"
	rec := httptest.NewRecorder()
	a.saveProject(rec, httptest.NewRequest("PUT", "/api/projects/p1", nil), p)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "不一致") {
		t.Fatalf("mismatched project address accepted: %d %s", rec.Code, rec.Body)
	}
}

func TestEnableManagedProjectRequiresReadyInitialization(t *testing.T) {
	a := &server{cfg: Config{Initializer: &fakeInitializer{ready: false}}}
	p := managedEnabledProject()
	p.ConnectionID = ""
	p.CredentialRef = "GITLAB_TOKEN"
	rec := httptest.NewRecorder()
	a.saveProject(rec, httptest.NewRequest("PUT", "/api/projects/p1", nil), p)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "初始化") {
		t.Fatalf("enable without ready initialization accepted: %d %s", rec.Code, rec.Body)
	}
}

func managedEnabledProject() domain.Project {
	return domain.Project{
		ID: "p1", Name: "P", Version: 1, Enabled: true, MetaMode: domain.MetaModeManaged, MetaRef: "main",
		GitLabURL:    "https://gitlab.example",
		Repositories: []domain.Repository{{ID: "svc", ProjectID: 1, URL: "https://gitlab.example/team/svc.git", Path: "svc", TargetBranch: "dev"}},
		Development:  domain.AgentConfig{Adapter: "opencode", Model: "p/m", Image: "img", TimeoutSeconds: 60},
		Review:       domain.AgentConfig{Adapter: "opencode", Model: "p/m", Image: "img", TimeoutSeconds: 60},
		Integration: domain.IntegrationConfig{ComposeFile: "integration/compose.yaml",
			Checks: []domain.Check{{Name: "t", Service: "runner", Command: []string{"true"}, Required: true}}},
		MaxReworks: 3, RequireCI: true,
	}
}

// Keeping the stored token while changing the GitLab address would send the old
// token to a different origin; a re-verified token is required.
func TestConnectionUpdateRejectsAddressChangeWithoutToken(t *testing.T) {
	box := newConnectionBox(t)
	sealed, err := box.Seal(mustJSON(t, credentials.Payload{Token: "glpat-old"}))
	if err != nil {
		t.Fatal(err)
	}
	conns := newFakeConnections()
	conns.items["c1"] = domain.Connection{ID: "c1", Name: "n", GitLabURL: "https://gitlab-a.example", Username: "u", Version: 1}
	conns.secrets["c1"] = sealed
	a := &server{cfg: Config{Box: box}, conns: conns}
	update := func(w http.ResponseWriter, r *http.Request) { a.connection(w, r, "c1", nil) }
	rec := callHandler(t, "PUT", "/api/connections/c1", map[string]any{"name": "n", "gitlabUrl": "https://gitlab-b.example", "version": 1}, update)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "Token") {
		t.Fatalf("address change with the old token accepted: %d %s", rec.Code, rec.Body)
	}
	remote := gitlabUserServer(t, 200, "operator")
	rec = callHandler(t, "PUT", "/api/connections/c1", map[string]any{"name": "n", "gitlabUrl": remote.URL, "version": 1, "token": "glpat-new"}, update)
	if rec.Code != 200 {
		t.Fatalf("verified token with a new address rejected: %d %s", rec.Code, rec.Body)
	}
	if conns.secrets["c1"] == sealed {
		t.Fatal("credential was not replaced after a verified address change")
	}
}
