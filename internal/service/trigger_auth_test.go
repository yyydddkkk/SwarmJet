package service

import (
	"context"
	"testing"
	"time"

	"swarmjet/internal/domain"
	"swarmjet/internal/forge"
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
