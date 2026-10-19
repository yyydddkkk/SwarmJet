package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func round4Env(t *testing.T) (*testEnv, domain.Hive) {
	t.Helper()
	env := newTestEnv(t, Options{})
	return env, env.seedHive("round4", "acme/round4")
}

func agentToken(t *testing.T, env *testEnv, agentID string) string {
	t.Helper()
	plaintext, _, err := env.backend.CreateAgentToken(context.Background(), agentID)
	if err != nil {
		t.Fatalf("CreateAgentToken(%s): %v", agentID, err)
	}
	return plaintext
}

func roleManifest(roles map[string]domain.Role, limits domain.Limits) domain.MethodologyManifest {
	return domain.MethodologyManifest{DefaultRole: "worker", Roles: roles, Limits: limits}
}

func TestSendHonoursMessagePermission(t *testing.T) {
	env, hive := round4Env(t)
	ctx := context.Background()
	env.backend.SetActiveMethodology(domain.Methodology{
		HiveID: hive.ID,
		Manifest: roleManifest(map[string]domain.Role{
			"worker": {Permissions: domain.RolePermissions{Message: true, Spawn: []string{"verifier"}}},
			"muted":  {Permissions: domain.RolePermissions{Message: false}},
		}, domain.Limits{}),
	})
	worker, err := env.backend.CreateForager(ctx, domain.Agent{HiveID: hive.ID, State: domain.AgentActive, RoleName: "worker", ChainID: "chain-1"})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := env.backend.CreateForager(ctx, domain.Agent{HiveID: hive.ID, State: domain.AgentActive, RoleName: "worker", ChainID: "chain-1"})
	if err != nil {
		t.Fatal(err)
	}
	muted, err := env.backend.CreateForager(ctx, domain.Agent{HiveID: hive.ID, State: domain.AgentActive, RoleName: "muted", ChainID: "chain-2"})
	if err != nil {
		t.Fatal(err)
	}
	// A dispatcher message makes the default target explicit for the muted
	// role: the local token that handed it the task.
	dispatcher := domain.LocalAddress(env.user.ID)
	if _, err := env.backend.SendMessage(ctx, domain.Message{
		HiveID: hive.ID, From: dispatcher, To: domain.ForagerAddress(muted.ID), Kind: domain.MessageTask, Body: "your task",
	}); err != nil {
		t.Fatal(err)
	}

	t.Run("permitted peer message", func(t *testing.T) {
		session := env.connect(agentToken(t, env, worker.ID))
		out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
			"hiveId": hive.ID, "to": "forager:" + peer.ID, "body": "peer update",
		}))
		if out.Message.To != string(domain.ForagerAddress(peer.ID)) {
			t.Fatalf("to = %q", out.Message.To)
		}
		if out.Message.Trust != string(domain.TrustAgent) {
			t.Fatalf("trust = %q, want agent", out.Message.Trust)
		}
	})

	t.Run("muted role may message its dispatcher", func(t *testing.T) {
		session := env.connect(agentToken(t, env, muted.ID))
		out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
			"hiveId": hive.ID, "body": "report to dispatcher",
		}))
		if out.Message.To != string(dispatcher) {
			t.Fatalf("to = %q, want dispatcher %q", out.Message.To, dispatcher)
		}
	})

	t.Run("muted role cannot message a peer", func(t *testing.T) {
		session := env.connect(agentToken(t, env, muted.ID))
		res := callTool(t, session, "send", map[string]any{
			"hiveId": hive.ID, "to": "forager:" + peer.ID, "body": "peer update",
		})
		if !res.IsError || !strings.Contains(toolErrorText(res), "foragers cannot message") {
			t.Fatalf("isError=%v text=%q", res.IsError, toolErrorText(res))
		}
	})

	t.Run("user token messages are user-trusted", func(t *testing.T) {
		session := env.connect(env.userToken)
		out := decode[sendOutput](t, callTool(t, session, "send", map[string]any{
			"hiveId": hive.ID, "to": "forager:" + peer.ID, "body": "from the human",
		}))
		if out.Message.Trust != string(domain.TrustUser) {
			t.Fatalf("trust = %q, want user", out.Message.Trust)
		}
	})
}

func TestSpawnChecksRolePermissionAndRecordsLineage(t *testing.T) {
	env, hive := round4Env(t)
	ctx := context.Background()
	env.backend.SetActiveMethodology(domain.Methodology{
		HiveID: hive.ID,
		Manifest: roleManifest(map[string]domain.Role{
			"worker":   {Permissions: domain.RolePermissions{Message: true, Spawn: []string{"verifier"}}},
			"verifier": {Permissions: domain.RolePermissions{Message: true}},
		}, domain.Limits{}),
	})
	worker, err := env.backend.CreateForager(ctx, domain.Agent{HiveID: hive.ID, State: domain.AgentActive, RoleName: "worker", ChainID: "chain-1"})
	if err != nil {
		t.Fatal(err)
	}
	session := env.connect(agentToken(t, env, worker.ID))

	out := decode[spawnOutput](t, callTool(t, session, "spawn", map[string]any{
		"hiveId": hive.ID, "role": "verifier", "task": "check revision", "revision": "abc123", "pr": 42,
	}))
	if out.Agent.RoleName != "verifier" || out.Agent.ParentID != worker.ID {
		t.Fatalf("agent = %+v", out.Agent)
	}
	if out.Agent.ChainID != "chain-1" {
		t.Fatalf("chain = %q, want inherited chain-1", out.Agent.ChainID)
	}
	if out.Agent.Revision != "abc123" || out.Agent.PRNumber != 42 {
		t.Fatalf("revision/pr = %q/%d", out.Agent.Revision, out.Agent.PRNumber)
	}
	if out.Message.Trust != string(domain.TrustAgent) || out.Message.Kind != "task" {
		t.Fatalf("task message = %+v", out.Message)
	}

	res := callTool(t, session, "spawn", map[string]any{
		"hiveId": hive.ID, "role": "worker", "task": "not allowed",
	})
	if !res.IsError || !strings.Contains(toolErrorText(res), "may not spawn") {
		t.Fatalf("disallowed spawn: isError=%v text=%q", res.IsError, toolErrorText(res))
	}

	user := env.connect(env.userToken)
	byUser := decode[spawnOutput](t, callTool(t, user, "spawn", map[string]any{
		"hiveId": hive.ID, "role": "auditor", "task": "anything goes",
	}))
	if byUser.Agent.RoleName != "auditor" || byUser.Agent.ParentID != "" {
		t.Fatalf("user spawn = %+v", byUser.Agent)
	}
	if byUser.Message.Trust != string(domain.TrustUser) {
		t.Fatalf("user spawn trust = %q, want user", byUser.Message.Trust)
	}
}

func TestSpawnAppliesMethodologyLimits(t *testing.T) {
	ctx := context.Background()

	t.Run("concurrency", func(t *testing.T) {
		env, hive := round4Env(t)
		env.backend.SetActiveMethodology(domain.Methodology{
			HiveID: hive.ID,
			Manifest: roleManifest(map[string]domain.Role{
				"worker":   {Permissions: domain.RolePermissions{Message: true, Spawn: []string{"verifier"}}},
				"verifier": {Permissions: domain.RolePermissions{Message: true}},
			}, domain.Limits{MaxConcurrentAgents: 1, MaxAgentsPerChain: 10}),
		})
		worker, err := env.backend.CreateForager(ctx, domain.Agent{HiveID: hive.ID, State: domain.AgentActive, RoleName: "worker", ChainID: "chain-1"})
		if err != nil {
			t.Fatal(err)
		}
		session := env.connect(agentToken(t, env, worker.ID))
		res := callTool(t, session, "spawn", map[string]any{"hiveId": hive.ID, "role": "verifier", "task": "too many"})
		if !res.IsError || !strings.Contains(toolErrorText(res), "concurrent") {
			t.Fatalf("isError=%v text=%q", res.IsError, toolErrorText(res))
		}
	})

	t.Run("chain", func(t *testing.T) {
		env, hive := round4Env(t)
		env.backend.SetActiveMethodology(domain.Methodology{
			HiveID: hive.ID,
			Manifest: roleManifest(map[string]domain.Role{
				"worker":   {Permissions: domain.RolePermissions{Message: true, Spawn: []string{"verifier"}}},
				"verifier": {Permissions: domain.RolePermissions{Message: true}},
			}, domain.Limits{MaxConcurrentAgents: 10, MaxAgentsPerChain: 1}),
		})
		worker, err := env.backend.CreateForager(ctx, domain.Agent{HiveID: hive.ID, State: domain.AgentActive, RoleName: "worker", ChainID: "chain-1"})
		if err != nil {
			t.Fatal(err)
		}
		session := env.connect(agentToken(t, env, worker.ID))
		res := callTool(t, session, "spawn", map[string]any{"hiveId": hive.ID, "role": "verifier", "task": "chain full"})
		if !res.IsError || !strings.Contains(toolErrorText(res), "chain") {
			t.Fatalf("isError=%v text=%q", res.IsError, toolErrorText(res))
		}
	})
}

func TestSpawnWithoutMethodologyIsPermissive(t *testing.T) {
	env, hive := round4Env(t)
	worker, err := env.backend.CreateForager(context.Background(), domain.Agent{HiveID: hive.ID, State: domain.AgentActive, ChainID: "chain-1"})
	if err != nil {
		t.Fatal(err)
	}
	session := env.connect(agentToken(t, env, worker.ID))
	out := decode[spawnOutput](t, callTool(t, session, "spawn", map[string]any{
		"hiveId": hive.ID, "role": "anything", "task": "no methodology yet",
	}))
	if out.Agent.ParentID != worker.ID || out.Agent.ChainID != "chain-1" {
		t.Fatalf("agent = %+v", out.Agent)
	}
}
