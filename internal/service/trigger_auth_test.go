package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
)

func TestLatestAutonomousAddUsesEffectiveEventAndActor(t *testing.T) {
	start := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	event := func(id int64, action, label string, actor int64, at time.Time) forge.LabelEvent {
		var e forge.LabelEvent
		e.ID, e.Action, e.CreatedAt, e.Label.Name, e.User.ID = id, action, at, label, actor
		return e
	}
	tests := []struct {
		name   string
		events []forge.LabelEvent
		want   int64
		ok     bool
	}{
		{"missing", nil, 0, false},
		{"newest by timestamp", []forge.LabelEvent{event(3, "add", "autonomous", 6, start.Add(time.Minute)), event(9, "remove", "autonomous", 5, start)}, 3, true},
		{"removed", []forge.LabelEvent{event(2, "remove", "autonomous", 6, start), event(1, "add", "autonomous", 5, start)}, 2, false},
		{"unknown actor", []forge.LabelEvent{event(2, "add", "autonomous", 0, start)}, 2, false},
		{"other label ignored", []forge.LabelEvent{event(1, "add", "autonomous", 5, start), event(9, "add", "other", 6, start.Add(time.Minute))}, 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := latestAutonomousAdd(tc.events)
			if got.ID != tc.want || ok != tc.ok {
				t.Fatalf("got event %d, valid=%v; want %d, %v", got.ID, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestGitHubAutoIntakeRequiresExplicitProjectSwitch(t *testing.T) {
	svc := &Service{}
	project := domain.Project{Provider: domain.ProviderGitHub}
	allowed, err := svc.authorizeInitialTrigger(context.Background(), nil, project, domain.Issue{})
	if err != nil || allowed {
		t.Fatalf("default policy admitted GitHub issue: allowed=%v err=%v", allowed, err)
	}
	project.GitHubAutoIntake = true
	allowed, err = svc.authorizeInitialTrigger(context.Background(), nil, project, domain.Issue{})
	if err != nil || !allowed {
		t.Fatalf("enabled policy rejected GitHub issue: allowed=%v err=%v", allowed, err)
	}
}

func TestGitLabAllModeAdmitsOnlyAutonomousIssuesWithoutActorLookup(t *testing.T) {
	svc := &Service{}
	project := domain.Project{Provider: domain.ProviderGitLab, GitLabTriggerMode: "all"}
	for _, tc := range []struct {
		name    string
		labels  []string
		allowed bool
	}{
		{"autonomous", []string{"autonomous"}, true},
		{"other labels", []string{"bug", "autonomous"}, true},
		{"unlabeled", nil, false},
		{"unrelated label", []string{"bug"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A nil forge client confirms that all mode does not require a label
			// event reader or a resolvable label actor.
			allowed, err := svc.authorizeInitialTrigger(context.Background(), nil, project, domain.Issue{Labels: tc.labels})
			if err != nil || allowed != tc.allowed {
				t.Fatalf("allowed=%v err=%v, want allowed=%v", allowed, err, tc.allowed)
			}
		})
	}
}

func TestScanAllModeClaimsAutonomousIssueWithoutLabelEvent(t *testing.T) {
	s := scanTestStore(t)
	ctx := context.Background()
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"project_id": 7, "iid": 1, "title": "labeled", "state": "opened", "labels": []string{"autonomous"}},
			{"project_id": 7, "iid": 2, "title": "other", "state": "opened", "labels": []string{"bug"}},
		})
	}))
	defer gitlab.Close()
	project := scanProject(gitlab.URL)
	project.GitLabTriggerMode = "all"
	if _, err := s.SaveProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: s, ResolveCredential: func(string) (string, error) { return "token", nil }, RequireNode: true}
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.Tasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].Issue.IID != 1 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
}
