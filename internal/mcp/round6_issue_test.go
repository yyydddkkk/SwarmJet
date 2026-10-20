package mcp

import (
	"context"
	"testing"
)

func TestSpawnKeepsSourceIssue(t *testing.T) {
	env, hive := round4Env(t)
	session := env.connect(env.userToken)
	out := decode[spawnOutput](t, callTool(t, session, "spawn", map[string]any{
		"hiveId": hive.ID, "task": "Resolve source issue", "issue": 73,
	}))
	a, err := env.backend.Agent(context.Background(), out.Agent.ID)
	if err != nil || a.IssueNumber != 73 || out.Agent.IssueNumber != 73 {
		t.Fatalf("source issue: %+v %v", a, err)
	}
	bad := callTool(t, session, "spawn", map[string]any{"hiveId": hive.ID, "task": "bad", "issue": -1})
	if !bad.IsError {
		t.Fatal("negative issue accepted")
	}
}
