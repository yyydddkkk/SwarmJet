package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLegacyProviderSnapshots(t *testing.T) {
	var task Task
	if err := json.Unmarshal([]byte(`{"project":{"gitlabUrl":"https://git.example"},"issue":{"projectId":12,"iid":3}}`), &task); err != nil {
		t.Fatal(err)
	}
	if task.Project.HostingProvider() != ProviderGitLab || task.Issue.HostingProvider() != ProviderGitLab || (Connection{}).HostingProvider() != ProviderGitLab {
		t.Fatal("legacy snapshots must remain GitLab")
	}
	if got := IssueSource(task.Project.HostingURL(), task.Issue); got != "https://git.example/12/3" {
		t.Fatal(got)
	}
}

func TestIssueSourceProviderIsolation(t *testing.T) {
	legacy := Issue{ProjectID: 12, IID: 3}
	if got := IssueSource("HTTPS://Git.Example/api/v4/", legacy); got != "https://git.example/12/3" {
		t.Fatal(got)
	}
	github := legacy
	github.Provider = ProviderGitHub
	if got := IssueSource("https://github.com", github); got != "github/12/3" {
		t.Fatal(got)
	}
	if IssueSource("https://github.com", github) == IssueSource("https://github.com", legacy) {
		t.Fatal("provider source collision")
	}
	if IssueSource("", github) != IssueSource("https://github.com", github) {
		t.Fatal("GitHub identity must not depend on legacy base URL")
	}
}

func TestGitHubDraftValidation(t *testing.T) {
	valid := Project{ID: "project", Name: "GitHub", Provider: ProviderGitHub, MetaMode: MetaModeManaged, ConnectionID: "app", Repositories: []Repository{{ID: "repo", ProjectID: 1, URL: "https://github.com/acme/repo.git"}}, AcceptanceStrategy: "platform_ci"}
	if err := ValidateProjectForSave(valid); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Project)
	}{
		{"imported", func(p *Project) { p.MetaMode = MetaModeImported }},
		{"no connection", func(p *Project) { p.ConnectionID = "" }},
		{"gitlab URL", func(p *Project) { p.GitLabURL = "https://gitlab.com" }},
		{"credential", func(p *Project) { p.CredentialRef = "TOKEN" }},
		{"foreign host", func(p *Project) { p.Repositories = []Repository{{URL: "https://github.com.evil/repo.git"}} }},
		{"unknown provider", func(p *Project) { p.Provider = "unknown" }},
		{"merge method", func(p *Project) { p.MergeMethod = "fast" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			tc.change(&p)
			if ValidateProjectForSave(p) == nil {
				t.Fatal("accepted invalid project")
			}
		})
	}
}

func TestGitHubConnectionMetadataOnly(t *testing.T) {
	c := Connection{ID: "app", Name: "App", Provider: ProviderGitHub, GitHubInstallationID: 42}
	if err := ValidateConnection(c); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "token") {
		t.Fatal("unexpected credential data")
	}
	c.GitHubInstallationID = 0
	if ValidateConnection(c) == nil {
		t.Fatal("accepted missing installation")
	}
}

func TestMergeQueueEntrySerialization(t *testing.T) {
	legacy, err := json.Marshal(Revision{RepositoryID: "repo"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), "mergeQueueEntryId") {
		t.Fatal("legacy revision gained queue metadata")
	}
	task := Task{Workspace: Workspace{Revisions: []Revision{{RepositoryID: "repo", MergeQueueEntryID: "MQE_123"}}}}
	data, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var restored Task
	if err = json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Workspace.Revisions[0].MergeQueueEntryID != "MQE_123" {
		t.Fatal("queue identity lost on task restore")
	}
}
