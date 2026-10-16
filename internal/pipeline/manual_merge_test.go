package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

func TestManualMergeReconcilesLostResponseAndPartialRound(t *testing.T) {
	s := isolatedPipelineStore(t)
	ctx := context.Background()
	var mu sync.Mutex
	merged := map[int]bool{}
	mergeCalls := map[int]int{}
	failSecond := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "personal-token" {
			t.Errorf("request omitted personal identity")
			w.WriteHeader(401)
			return
		}
		var repo int
		if strings.Contains(r.URL.Path, "/projects/1/") {
			repo = 1
		} else if strings.Contains(r.URL.Path, "/projects/2/") {
			repo = 2
		}
		if repo == 0 {
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/closes_issues"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/repository/branches/main"):
			_, _ = fmt.Fprintf(w, `{"name":"main","commit":{"id":"base-%d"}}`, repo)
		case strings.HasSuffix(r.URL.Path, "/merge") && r.Method == http.MethodPut:
			var body struct {
				SHA string `json:"sha"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.SHA != fmt.Sprintf("head-%d", repo) {
				t.Errorf("merge did not pin source SHA: %+v", body)
				w.WriteHeader(409)
				return
			}
			mergeCalls[repo]++
			if repo == 2 && failSecond {
				w.WriteHeader(502)
				_, _ = w.Write([]byte(`{"message":"upstream unavailable"}`))
				return
			}
			merged[repo] = true
			if repo == 1 {
				// GitLab commits the first merge, then the connection drops.
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case strings.Contains(r.URL.Path, "/merge_requests/"):
			state := "opened"
			if merged[repo] {
				state = "merged"
			}
			_, _ = fmt.Fprintf(w, `{"iid":%d,"sha":"head-%d","source_branch":"branch-%d","target_branch":"main","state":%q,"detailed_merge_status":"mergeable"}`, repo*11, repo, repo, state)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	project := domain.Project{ID: "project", GitLabURL: server.URL, AcceptanceStrategy: "none", Repositories: []domain.Repository{{ID: "one", ProjectID: 1}, {ID: "two", ProjectID: 2}}}
	projectJSON, _ := json.Marshal(project)
	if _, err := s.Pool.Exec(ctx, `INSERT INTO projects(id,version,body) VALUES($1,1,$2)`, project.ID, projectJSON); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "manual", Project: project, Issue: domain.Issue{ProjectID: 1, IID: 9}, Stage: domain.StageWaitingManualMerge, Executions: map[string]domain.Execution{}, Workspace: domain.Workspace{Revisions: []domain.Revision{
		{RepositoryID: "one", ProjectID: 1, MRIID: 11, BaseSHA: "base-1", HeadSHA: "head-1", TargetBranch: "main", Branch: "branch-1", Changed: true},
		{RepositoryID: "two", ProjectID: 2, MRIID: 22, BaseSHA: "base-2", HeadSHA: "head-2", TargetBranch: "main", Branch: "branch-2", Changed: true},
	}}}
	task.ReviewDigest = domain.Digest(task.Workspace.Revisions)
	taskJSON, _ := json.Marshal(task)
	if _, err := s.Pool.Exec(ctx, `INSERT INTO tasks(id,project_id,source,stage,body) VALUES($1,$2,$3,$4,$5)`, task.ID, project.ID, domain.IssueSource(project.GitLabURL, task.Issue), task.Stage, taskJSON); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveManualAcceptance(ctx, task.ID, structuralAcceptance(task)); err != nil {
		t.Fatal(err)
	}
	task.Stage = domain.StageManualMerging
	if err := s.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	a := Activities{Store: s, PersonalGitLabToken: func(context.Context, string) (string, error) { return "personal-token", nil }}
	if _, err := a.manualMergeRound(ctx, task); err == nil {
		t.Fatal("second repository failure was ignored")
	}
	saved, err := s.Task(ctx, task.ID)
	if err != nil || !saved.Workspace.Revisions[0].Merged || saved.Workspace.Revisions[1].Merged {
		t.Fatalf("partial result not persisted: %+v %v", saved.Workspace.Revisions, err)
	}
	mu.Lock()
	failSecond = false
	mu.Unlock()
	result, err := a.manualMergeRound(ctx, task) // Temporal may retry with its original Task snapshot.
	if err != nil || !result.Ready || !result.Task.Workspace.Revisions[0].Merged || !result.Task.Workspace.Revisions[1].Merged {
		t.Fatalf("reconciliation failed: %+v %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if mergeCalls[1] != 1 || mergeCalls[2] != 2 {
		t.Fatalf("remote writes were replayed: %+v", mergeCalls)
	}
}

func structuralAcceptance(task domain.Task) store.ManualAcceptanceRecord {
	return store.ManualAcceptanceRecord{Digest: domain.ManualMergeEvidenceDigest(task), Checks: []store.ManualAcceptanceCheck{}, Note: "verified"}
}
