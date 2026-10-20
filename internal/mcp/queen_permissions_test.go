package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
)

func TestQueenMethodologyManagementIgnoresRolePermissions(t *testing.T) {
	env := newAgentScopeEnv(t)
	queen := env.connect(env.queenToken)
	for i := 0; i < 2; i++ {
		out := decode[publishMethodologyOutput](t, callTool(t, queen, "publish_methodology", map[string]any{
			"hiveId": env.hive.ID, "manifest": methodologyYAML("Queen can repair this", false),
		}))
		if out.Methodology.Version != i+1 || out.Methodology.CreatedBy != domain.AddressQueen {
			t.Fatalf("published = %+v", out.Methodology)
		}
	}
	callTool(t, queen, "get_methodology", map[string]any{"hiveId": env.hive.ID})
	callTool(t, queen, "list_methodology_versions", map[string]any{"hiveId": env.hive.ID})
	out := decode[activateMethodologyOutput](t, callTool(t, queen, "activate_methodology", map[string]any{"hiveId": env.hive.ID, "version": 1}))
	if out.Methodology.Version != 1 {
		t.Fatalf("activated = %+v", out.Methodology)
	}
	for _, tool := range []string{"publish_methodology", "activate_methodology"} {
		res, err := callToolRaw(t, queen, tool, map[string]any{"hiveId": env.otherHive.ID, "manifest": methodologyYAML("other", false), "version": 1})
		if !denied(t, res, err) {
			t.Fatalf("Queen could %s in another Hive", tool)
		}
	}
}

func TestQueenFixedAbilitiesIgnoreMethodologyRole(t *testing.T) {
	env := newAgentScopeEnv(t)
	queen := env.connect(env.queenToken)
	// The Queen has no methodology role. Even the default role has no abilities.
	_, err := env.backend.PublishMethodology(context.Background(), domain.Methodology{
		HiveID: env.hive.ID,
		Manifest: domain.MethodologyManifest{DefaultRole: "restricted", Roles: map[string]domain.Role{
			"restricted": {}, "reviewer": {},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"restricted", "reviewer"} {
		out := decode[spawnOutput](t, callTool(t, queen, "spawn", map[string]any{"hiveId": env.hive.ID, "role": role, "task": "inspect"}))
		if out.Agent.RoleName != role {
			t.Fatalf("spawned role = %q, want %q", out.Agent.RoleName, role)
		}
	}
	res, err := callToolRaw(t, queen, "spawn", map[string]any{"hiveId": env.hive.ID, "role": "missing", "task": "inspect"})
	if !denied(t, res, err) {
		t.Fatal("Queen spawned undefined role")
	}
	callTool(t, queen, "send", map[string]any{"hiveId": env.hive.ID, "to": string(domain.ForagerAddress(env.peer.ID)), "body": "inspect"})
}

func TestQueenWithoutMethodologyMayOnlySpawnWorker(t *testing.T) {
	env := newAgentScopeEnv(t)
	queen := env.connect(env.queenToken)
	callTool(t, queen, "spawn", map[string]any{"hiveId": env.hive.ID, "task": "inspect"})
	res, err := callToolRaw(t, queen, "spawn", map[string]any{"hiveId": env.hive.ID, "role": "reviewer", "task": "inspect"})
	if !denied(t, res, err) {
		t.Fatal("Queen spawned non-worker without a methodology")
	}
}

func TestQueenMergeUsesQueenSettingAndPreservesMergeGates(t *testing.T) {
	svc := &fakeMergeService{
		permissions: map[string]domain.RolePermissions{"worker": {Merge: true}},
		outcome:     merge.Outcome{Merged: true},
	}
	env := newTestEnv(t, Options{Merge: svc})
	hive := env.seedHive("Queen merge", "acme/queen")
	queen, err := env.backend.Queen(context.Background(), hive.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := env.backend.CreateAgentToken(context.Background(), queen.ID)
	if err != nil {
		t.Fatal(err)
	}
	session := env.connect(token)
	args := map[string]any{"hiveId": hive.ID, "pr": 7}
	res, err := callToolRaw(t, session, "merge_pr", args)
	if !denied(t, res, err) {
		t.Fatal("Queen merged without methodology")
	}
	for _, enabled := range []bool{false, true, false} {
		_, err := env.backend.PublishMethodology(context.Background(), domain.Methodology{HiveID: hive.ID, Manifest: domain.MethodologyManifest{
			DefaultRole: "worker", Roles: map[string]domain.Role{"worker": {}}, Queen: domain.QueenStyle{Merge: enabled},
		}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !enabled {
			res, err := callToolRaw(t, session, "merge_pr", args)
			if !denied(t, res, err) {
				t.Fatal("Queen merged with queen.merge=false despite role merge permission")
			}
			continue
		}
		// No role permission lookup is needed, even if that service would fail.
		svc.permissionErr = errors.New("Queen has no methodology role")
		out := decode[mergePROutput](t, callTool(t, session, "merge_pr", args))
		if !out.Merged {
			t.Fatalf("merge = %+v", out)
		}
		svc.mergeErr = &merge.NotReadyError{Readiness: merge.Readiness{Reasons: []string{"checks pending"}}}
		out = decode[mergePROutput](t, callTool(t, session, "merge_pr", args))
		if out.Merged || out.Ready || len(out.Reasons) != 1 || out.Reasons[0] != "checks pending" {
			t.Fatalf("merge gates bypassed: %+v", out)
		}
	}
}

func TestQueenCatalogToolsAuthorized(t *testing.T) {
	queen := domain.Agent{ID: "queen", Role: domain.RoleQueen}
	ts := &toolset{token: domain.Token{Scope: domain.TokenAgent}, actor: actor{agent: &queen}}
	for _, tool := range []string{"list_methodology_templates", "install_methodology_template"} {
		if err := ts.authorize(tool); err != nil || !ts.visible(tool) {
			t.Fatalf("Queen catalog permission for %s: %v", tool, err)
		}
	}
}
