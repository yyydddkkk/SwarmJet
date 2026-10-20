package router

import (
	"context"
	"errors"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
)

func TestReconcileParkedPRUsesForgeStateAndOwnerBranch(t *testing.T) {
	for _, tc := range []struct {
		name string
		state string
		merged bool
		branch string
		readErr error
		wantMessages int
	}{
		{"merged", "closed", true, "swarm/owner", nil, 2},
		{"closed", "closed", false, "swarm/owner", nil, 2},
		{"open", "open", false, "swarm/owner", nil, 0},
		{"unrelated", "closed", true, "swarm/other", nil, 0},
		{"unknown branch", "closed", true, "", nil, 0},
		{"forge unavailable", "", false, "", errors.New("unavailable"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewMemoryBackend()
			hive := newTestHive(t, b, domain.HiveConfig{})
			owner := b.AddForager(domain.Agent{ID: "owner", HiveID: hive.ID, Role: domain.RoleForager, State: domain.AgentParked, PRNumber: 42, Branch: "swarm/owner"})
			r := New(b)
			read := func(ctx context.Context, repository string, pr int64) (github.PullState, error) {
				if repository != hive.Repository || pr != 42 {
					t.Fatalf("wrong forge lookup: %s %d", repository, pr)
				}
				return github.PullState{Number: pr, State: tc.state, Merged: tc.merged, HeadRef: tc.branch, HeadSHA: "sha"}, tc.readErr
			}
			for i := 0; i < 2; i++ {
				if err := r.ReconcileParkedPR(context.Background(), hive, owner, read); !errors.Is(err, tc.readErr) {
					t.Fatalf("reconcile = %v", err)
				}
			}
			messages := b.MessagesTo(hive.ID, domain.ForagerAddress(owner.ID))
			if len(messages) != tc.wantMessages {
				t.Fatalf("messages = %+v", messages)
			}
			if tc.wantMessages > 0 && (messages[0].Kind != domain.MessageControl || messages[0].Trust != domain.TrustSystem || messages[1].Kind != domain.MessageEvent) {
				t.Fatalf("closure lifecycle messages = %+v", messages)
			}
		})
	}
}
