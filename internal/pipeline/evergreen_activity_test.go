package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/execution"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

type cleanupOnlyNode struct {
	execution.Node
	cleaned bool
}

func (n *cleanupOnlyNode) Cleanup(context.Context, string, domain.Workspace, domain.IntegrationConfig, map[string]string) (bool, error) {
	n.cleaned = true
	return true, nil
}

type fixedNodeResolver struct{ node execution.Node }

func (r fixedNodeResolver) Node(context.Context, string) (execution.Node, error) { return r.node, nil }

func TestEvergreenFinalizationLeavesIssueOpen(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "pipeline_evergreen_" + hex.EncodeToString(random)
	if _, err := admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
	config := admin.Pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &store.Store{Pool: pool}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	closed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "PUT" {
			closed = true
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/links") {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/notes") {
			if r.Method == "GET" {
				_, _ = w.Write([]byte(`[]`))
			} else {
				_, _ = w.Write([]byte(`{}`))
			}
			return
		}
		if r.URL.Path == "/api/v4/projects/1/issues/1" {
			_ = json.NewEncoder(w).Encode(map[string]any{"project_id": 1, "iid": 1, "state": "opened"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"iid": 1, "sha": "head", "source_branch": "branch", "target_branch": "main", "state": "merged"})
	}))
	defer server.Close()
	node := &cleanupOnlyNode{}
	a := &Activities{Store: s, Nodes: fixedNodeResolver{node: node}, ProjectCredential: func(domain.Project, bool) (string, error) { return "token", nil }}
	task := domain.Task{ID: "task", Round: 1, NodeID: "node", Stage: domain.StageFinalizing, Project: domain.Project{GitLabURL: server.URL}, Issue: domain.Issue{ProjectID: 1, IID: 1, State: "opened"}, Workspace: domain.Workspace{Revisions: []domain.Revision{{RepositoryID: "api", ProjectID: 1, MRIID: 1, HeadSHA: "head", Branch: "branch", TargetBranch: "main", Changed: true, Merged: true, Sensitive: true}}}}
	result, err := a.Step(ctx, StepInput{Phase: domain.StageFinalizing, Task: task})
	if err != nil || closed || !node.cleaned || result.Task.Issue.State != "opened" {
		t.Fatalf("result=%+v closed=%v cleaned=%v err=%v", result, closed, node.cleaned, err)
	}
	upstream := domain.Task{ID: "upstream", Stage: domain.StageCompleted, Issue: domain.Issue{ProjectID: 1, IID: 2}, CreatedAt: time.Now().UTC()}
	upstreamBody, _ := json.Marshal(upstream)
	if _, err := s.Pool.Exec(ctx, `INSERT INTO projects(id,version,body) VALUES('project',1,'{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO tasks(id,project_id,source,stage,terminal,body) VALUES($1,'project',$2,'completed',true,$3)`, upstream.ID, domain.GitLabIdentity(server.URL)+"/1/2", upstreamBody); err != nil {
		t.Fatal(err)
	}
	downstream := domain.Task{ID: "downstream", Round: 1, Project: domain.Project{GitLabURL: server.URL}, Issue: domain.Issue{ProjectID: 1, IID: 1}, Dependencies: []domain.IssueRef{{ProjectID: 1, IID: 2}}, Stage: domain.StageWaitingDependencies}
	ready, err := a.Step(ctx, StepInput{Phase: domain.StageWaitingDependencies, Task: downstream})
	if err != nil || !ready.Ready || ready.Task.DependencyTasks["1/2"] != "upstream" {
		t.Fatalf("open upstream issue did not satisfy completed task dependency: %+v %v", ready, err)
	}
	old := domain.Task{ID: "old", Stage: domain.StageCompleted, Project: domain.Project{GitLabURL: server.URL}, Issue: domain.Issue{ProjectID: 1, IID: 1}, CreatedAt: time.Now().UTC().Add(-time.Minute)}
	newer := domain.Task{ID: "newer", Stage: domain.StageQueued, Project: old.Project, Issue: old.Issue, CreatedAt: time.Now().UTC()}
	for _, item := range []domain.Task{old, newer} {
		body, _ := json.Marshal(item)
		if _, err := s.Pool.Exec(ctx, `INSERT INTO tasks(id,project_id,source,stage,terminal,body) VALUES($1,'project',$2,$3,$4,$5)`, item.ID, domain.GitLabIdentity(server.URL)+"/1/1", item.Stage, item.Stage == domain.StageCompleted, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Sync(ctx, old); err != nil || closed {
		t.Fatalf("old round overwrote current Issue label: closed=%v err=%v", closed, err)
	}
}
