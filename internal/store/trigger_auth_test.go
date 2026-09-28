package store

import (
	"context"
	"testing"

	"swarmjet/internal/domain"
)

func TestAuthorizedGitLabActorRequiresActiveBoundProjectRole(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.SaveProject(ctx, testProject()); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"owner", "operator", "viewer"} {
		_, err := s.Pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,status) VALUES($1,$2,$1,'hash','active')`, user, user+"@example.test")
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Pool.Exec(ctx, `INSERT INTO external_identities(user_id,provider,instance,external_id,username) VALUES($1,'gitlab',$2,$3,$1)`, user, "https://git.example", map[string]string{"owner": "11", "operator": "12", "viewer": "13"}[user])
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Pool.Exec(ctx, `INSERT INTO project_grants(project_id,user_id,role) VALUES('project',$1,$1)`, user)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		id             int64
		operator, want bool
	}{{11, false, true}, {12, false, false}, {12, true, true}, {13, true, false}, {99, true, false}} {
		got, err := s.AuthorizedGitLabActor(ctx, "project", "https://git.example/api/v4", tc.id, tc.operator)
		if err != nil || got != tc.want {
			t.Fatalf("actor=%d operator=%v got=%v err=%v", tc.id, tc.operator, got, err)
		}
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE users SET status='disabled' WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	got, err := s.AuthorizedGitLabActor(ctx, "project", "https://git.example", 11, true)
	if err != nil || got {
		t.Fatalf("disabled actor authorized: %v, %v", got, err)
	}
	issue := domain.Issue{ProjectID: 1, IID: 2}
	if err := s.RecordDeniedIssueLabelEvent(ctx, "https://git.example", issue, 5, "denied"); err != nil {
		t.Fatal(err)
	}
	denied, err := s.DeniedIssueLabelEvent(ctx, "https://git.example", issue, 5)
	if err != nil || !denied {
		t.Fatalf("denial missing: %v, %v", denied, err)
	}
	for range 2 {
		if err := s.RecordExternalTriggerAudit(ctx, "project", "gitlab", "issue-source", "label", 5, 11, "denied", "not a member"); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.AuditEvents(ctx, "project", 10)
	if err != nil || len(events) != 1 || events[0].Action != "trigger.label.denied" {
		t.Fatalf("trigger audit was not idempotent: %+v %v", events, err)
	}
}
