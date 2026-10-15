package execution

import (
	"context"
	"net/http/httptest"
	"os/exec"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/workspace"
	"testing"
)

type publishingNode struct {
	Node
	tokens    []string
	revisions []domain.Revision
}

func (n *publishingNode) PushWorkspace(_ context.Context, ws domain.Workspace, _ domain.Project, env map[string]string) error {
	n.tokens = append(n.tokens, env["GITHUB_TOKEN"])
	n.revisions = ws.Revisions
	return nil
}

func TestRemotePublicationReceivesFreshCallCredentials(t *testing.T) {
	node := &publishingNode{}
	server := httptest.NewServer(&Handler{Node: node, Secret: "shared-secret"})
	defer server.Close()
	remote := NewRemoteNode("node", server.URL, "shared-secret", server.Client())
	ws := domain.Workspace{TaskID: "task", Revisions: []domain.Revision{{RepositoryID: "repo", Branch: "bot/task", HeadSHA: "inspected"}}}
	// A long-running agent can outlive its initial installation token. A later
	// publication call transports the new credential without retaining the old one.
	for _, token := range []string{"initial-installation-token", "renewed-installation-token"} {
		if err := remote.PushWorkspace(context.Background(), ws, domain.Project{ID: "project"}, map[string]string{"GITHUB_TOKEN": token}); err != nil {
			t.Fatal(err)
		}
	}
	if len(node.tokens) != 2 || node.tokens[1] != "renewed-installation-token" || node.revisions[0].HeadSHA != "inspected" {
		t.Fatal("publication request lost identity or fresh credential")
	}
}

func TestWorkspaceCallCredentialsDoNotMutateNodeDefaults(t *testing.T) {
	manager := workspace.New(t.TempDir(), "")
	manager.Env = map[string]string{"GIT_ASKPASS": "/node/askpass", "GITHUB_TOKEN": "old"}
	node := &LocalNode{NodeID: "node", Workspace: manager}
	call := node.workspace(map[string]string{"GITHUB_TOKEN": "renewed", "GIT_ASKPASS": "/control/askpass"})
	if call.Env["GITHUB_TOKEN"] != "renewed" || call.Env["GIT_ASKPASS"] != "/node/askpass" {
		t.Fatal("incorrect credential or helper selected")
	}
	if manager.Env["GITHUB_TOKEN"] != "old" {
		t.Fatal("per-call credential persisted in node defaults")
	}
}

func TestAskpassSelectsInstallationCredential(t *testing.T) {
	for _, test := range []struct {
		prompt, want string
		env          []string
	}{
		{"Username for https://github.com", "x-access-token\n", []string{"GITHUB_TOKEN=installation"}},
		{"Password for https://github.com", "installation\n", []string{"GITHUB_TOKEN=installation", "GITLAB_TOKEN=legacy"}},
		{"Username for https://gitlab.example", "oauth2\n", []string{"GITLAB_TOKEN=legacy"}},
		{"Password for https://gitlab.example", "legacy\n", []string{"GITLAB_TOKEN=legacy"}},
	} {
		command := exec.Command("/bin/sh", "../../scripts/askpass.sh", test.prompt)
		command.Env = test.env
		got, err := command.Output()
		if err != nil || string(got) != test.want {
			t.Fatalf("askpass selection failed for %s: %v", test.prompt, err)
		}
	}
}
