package store

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestMigrateRemovesPlatformUpgradeTaskGate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var gates int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname='platform_upgrade_task_gate'`).Scan(&gates); err != nil {
		t.Fatal(err)
	}
	if gates != 0 {
		t.Fatalf("platform upgrade task gate still installed: %d", gates)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var jobs int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateFromFiveAddsSessionAndAgentTokens(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	h := newTestHive(t, s, "acme/v5")
	a, err := s.CreateForager(ctx, domain.Agent{HiveID: h.ID})
	if err != nil {
		t.Fatal(err)
	}
	plaintext, oldToken, err := s.CreateToken(ctx, "before-upgrade", domain.TokenAdmin)
	if err != nil {
		t.Fatal(err)
	}
	// Rebuild the version-5 shape while keeping the old rows intact.
	for _, statement := range []string{
		`DELETE FROM schema_version WHERE version > 5`,
		`DROP INDEX IF EXISTS mcp_tokens_by_agent`,
		`ALTER TABLE agents DROP COLUMN cli_session_ref`,
		`ALTER TABLE mcp_tokens DROP COLUMN agent_id, DROP COLUMN revoked_at`,
	} {
		if _, err = s.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var version int
	if err = s.Pool.QueryRow(ctx, `SELECT max(version) FROM schema_version`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	got, err := s.Agent(ctx, a.ID)
	if err != nil || got.SessionRef != "" || got.Branch != a.Branch {
		t.Fatalf("old agent after upgrade: %+v %v", got, err)
	}
	old, err := s.TokenByPlaintext(ctx, plaintext)
	if err != nil || old.ID != oldToken.ID || old.AgentID != "" {
		t.Fatalf("old token after upgrade: %+v %v", old, err)
	}
	// The new columns are writable after the upgrade.
	if err = s.SetAgentSession(ctx, a.ID, "resumed"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetAgentPR(ctx, a.ID, 12); err != nil {
		t.Fatal(err)
	}
	plaintext, agentToken, err := s.CreateAgentToken(ctx, a.ID)
	if err != nil || agentToken.AgentID != a.ID {
		t.Fatalf("%+v %v", agentToken, err)
	}
	if _, err = s.TokenByPlaintext(ctx, plaintext); err != nil {
		t.Fatal(err)
	}
}
