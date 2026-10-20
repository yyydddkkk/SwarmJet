package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func methodologyYAML(standingOrder string, methodologyPermission bool) string {
	permission := "false"
	if methodologyPermission {
		permission = "true"
	}
	return `defaultRole: worker
standingOrders: ["` + standingOrder + `"]
roles:
  worker:
    description: Write code and open a pull request.
    permissions: { publish: true, message: true, methodology: ` + permission + ` }
merge:
  requireChecks: true
`
}

func TestUserCanPublishListGetAndActivateMethodology(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("Methodology", "acme/methodology")
	session := env.connect(env.userToken)

	first := decode[publishMethodologyOutput](t, callTool(t, session, "publish_methodology", map[string]any{
		"hiveId":   h.ID,
		"manifest": methodologyYAML("first order", false),
		"source":   map[string]any{"url": "https://github.com/acme/pstack", "ref": "main", "path": "pstack", "sha": "abc123"},
		"note":     "initial",
	}))
	if first.Methodology.Version != 1 || !first.Methodology.Active || first.Methodology.Builtin {
		t.Fatalf("first publish = %+v", first.Methodology)
	}
	if first.Methodology.CreatedBy != domain.LocalAddress(env.user.ID) {
		t.Fatalf("createdBy = %q", first.Methodology.CreatedBy)
	}
	if first.Methodology.Source == nil || first.Methodology.Source.URL != "https://github.com/acme/pstack" ||
		first.Methodology.Source.SHA != "abc123" {
		t.Fatalf("source = %+v", first.Methodology.Source)
	}

	second := decode[publishMethodologyOutput](t, callTool(t, session, "publish_methodology", map[string]any{
		"hiveId":   h.ID,
		"manifest": methodologyYAML("second order", false),
		"files":    []map[string]string{{"path": "skills/testing/SKILL.md", "content": "run tests"}},
		"note":     "second",
	}))
	if second.Methodology.Version != 2 {
		t.Fatalf("second publish = %+v", second.Methodology)
	}

	active := decode[getMethodologyOutput](t, callTool(t, session, "get_methodology", map[string]any{
		"hiveId": h.ID, "includeFiles": true,
	}))
	if active.Methodology.Version != 2 || active.Methodology.Builtin {
		t.Fatalf("active = %+v", active.Methodology)
	}
	if got := active.Methodology.Manifest.StandingOrders; len(got) != 1 || got[0] != "second order" {
		t.Fatalf("standing orders = %v", got)
	}
	if len(active.Methodology.Files) != 1 || active.Methodology.Files[0].Path != "skills/testing/SKILL.md" ||
		active.Methodology.Files[0].Content != "run tests" {
		t.Fatalf("files = %+v", active.Methodology.Files)
	}

	versions := decode[listMethodologyVersionsOutput](t, callTool(t, session, "list_methodology_versions", map[string]any{"hiveId": h.ID}))
	if versions.ActiveVersion != 2 || len(versions.Versions) != 2 || len(versions.Changes) != 2 {
		t.Fatalf("versions = %+v", versions)
	}
	if versions.Changes[0].Action != "publish" || versions.Changes[0].Note != "initial" ||
		versions.Changes[0].Actor != domain.LocalAddress(env.user.ID) {
		t.Fatalf("change record = %+v", versions.Changes[0])
	}

	rolledBack := decode[activateMethodologyOutput](t, callTool(t, session, "activate_methodology", map[string]any{
		"hiveId": h.ID, "version": 1, "note": "rollback",
	}))
	if rolledBack.Methodology.Version != 1 || !rolledBack.Methodology.Active {
		t.Fatalf("activate = %+v", rolledBack.Methodology)
	}
	active = decode[getMethodologyOutput](t, callTool(t, session, "get_methodology", map[string]any{"hiveId": h.ID}))
	if active.Methodology.Version != 1 || active.Methodology.Manifest.StandingOrders[0] != "first order" {
		t.Fatalf("active after rollback = %+v", active.Methodology)
	}
	versions = decode[listMethodologyVersionsOutput](t, callTool(t, session, "list_methodology_versions", map[string]any{"hiveId": h.ID}))
	if versions.ActiveVersion != 1 || len(versions.Changes) != 3 {
		t.Fatalf("versions after rollback = %+v", versions)
	}
	if versions.Changes[2].Action != "activate" || versions.Changes[2].Version != 1 {
		t.Fatalf("activate record = %+v", versions.Changes[2])
	}

	// Every publish and activation leaves one system message in the Hive Inbox.
	inbox, err := env.backend.Inbox(context.Background(), h.ID, domain.AddressHive, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 3 {
		t.Fatalf("inbox = %d messages, want 3", len(inbox))
	}
	for _, message := range inbox {
		if message.From != domain.AddressSystem || message.Trust != domain.TrustSystem ||
			!strings.Contains(message.Body, "methodology") {
			t.Fatalf("system message = %+v", message)
		}
	}
}

func TestGetMethodologyFallsBackToBuiltin(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("Empty", "acme/empty")
	session := env.connect(env.userToken)

	out := decode[getMethodologyOutput](t, callTool(t, session, "get_methodology", map[string]any{"hiveId": h.ID}))
	if !out.Methodology.Builtin || out.Methodology.Manifest.DefaultRole != "worker" || out.Methodology.Version != 0 {
		t.Fatalf("builtin fallback = %+v", out.Methodology)
	}
	if !out.Methodology.Manifest.Roles["worker"].Permissions.Publish ||
		out.Methodology.Manifest.Roles["worker"].Permissions.Attest {
		t.Fatalf("builtin role = %+v", out.Methodology.Manifest.Roles["worker"])
	}
}

func TestMethodologyWriteRequiresRolePermission(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("Permissions", "acme/permissions")
	user := env.connect(env.userToken)

	agent, err := env.backend.CreateForager(context.Background(), domain.Agent{
		HiveID: h.ID, Role: domain.RoleForager, RoleName: "worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	plaintext, _, err := env.backend.CreateAgentToken(context.Background(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	agentSession := env.connect(plaintext)

	callTool(t, user, "publish_methodology", map[string]any{
		"hiveId": h.ID, "manifest": methodologyYAML("no permission", false), "note": "baseline",
	})

	// Reads are open to agent tokens.
	read := callTool(t, agentSession, "get_methodology", map[string]any{"hiveId": h.ID})
	if read.IsError {
		t.Fatalf("agent could not read the methodology: %s", toolErrorText(read))
	}
	list := callTool(t, agentSession, "list_methodology_versions", map[string]any{"hiveId": h.ID})
	if list.IsError {
		t.Fatalf("agent could not list versions: %s", toolErrorText(list))
	}

	denied := callTool(t, agentSession, "publish_methodology", map[string]any{
		"hiveId": h.ID, "manifest": methodologyYAML("agent version", false), "note": "denied",
	})
	if !denied.IsError || !strings.Contains(toolErrorText(denied), "permission denied") {
		t.Fatalf("agent without permission published: isError=%v text=%q", denied.IsError, toolErrorText(denied))
	}
	deniedActivate := callTool(t, agentSession, "activate_methodology", map[string]any{
		"hiveId": h.ID, "version": 1,
	})
	if !deniedActivate.IsError || !strings.Contains(toolErrorText(deniedActivate), "permission denied") {
		t.Fatalf("agent without permission activated: isError=%v text=%q", deniedActivate.IsError, toolErrorText(deniedActivate))
	}

	// Publishing a version that grants the role the permission takes effect
	// for the agent's very next call.
	callTool(t, user, "publish_methodology", map[string]any{
		"hiveId": h.ID, "manifest": methodologyYAML("can publish", true), "note": "grant",
	})
	allowed := decode[publishMethodologyOutput](t, callTool(t, agentSession, "publish_methodology", map[string]any{
		"hiveId": h.ID, "manifest": methodologyYAML("agent version", true), "note": "agent publish",
	}))
	if allowed.Methodology.Version != 3 || allowed.Methodology.CreatedBy != domain.ForagerAddress(agent.ID) {
		t.Fatalf("agent publish = %+v", allowed.Methodology)
	}
}

func TestAgentCannotReadAnotherHiveMethodology(t *testing.T) {
	env := newTestEnv(t, Options{})
	own := env.seedHive("Own", "acme/own")
	other := env.seedHive("Other", "acme/other")
	agent, err := env.backend.CreateForager(context.Background(), domain.Agent{
		HiveID: own.ID, Role: domain.RoleForager, RoleName: "worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	plaintext, _, err := env.backend.CreateAgentToken(context.Background(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	session := env.connect(plaintext)
	res := callTool(t, session, "get_methodology", map[string]any{"hiveId": other.ID})
	if !res.IsError || !strings.Contains(toolErrorText(res), "can only access Hive") {
		t.Fatalf("cross-hive read: isError=%v text=%q", res.IsError, toolErrorText(res))
	}
}

func TestPublishMethodologyRejectsInvalidPackages(t *testing.T) {
	env := newTestEnv(t, Options{})
	h := env.seedHive("Invalid", "acme/invalid")
	session := env.connect(env.userToken)

	cases := []struct {
		name string
		in   map[string]any
	}{
		{"unknown field", map[string]any{"manifest": methodologyYAML("x", false) + "mystery: true\n"}},
		{"missing prompt file", map[string]any{"manifest": `defaultRole: worker
roles:
  worker:
    prompt: roles/worker.md
    permissions: { publish: true }
`}},
		{"unsafe file path", map[string]any{
			"manifest": methodologyYAML("x", false),
			"files":    []map[string]string{{"path": "../escape.md", "content": "x"}},
		}},
		{"duplicate file", map[string]any{
			"manifest": methodologyYAML("x", false),
			"files": []map[string]string{
				{"path": "a.md", "content": "x"},
				{"path": "a.md", "content": "y"},
			},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.in["hiveId"] = h.ID
			res := callTool(t, session, "publish_methodology", tc.in)
			if !res.IsError {
				t.Fatal("invalid package was published")
			}
		})
	}
	versions := decode[listMethodologyVersionsOutput](t, callTool(t, session, "list_methodology_versions", map[string]any{"hiveId": h.ID}))
	if len(versions.Versions) != 0 || len(versions.Changes) != 0 {
		t.Fatalf("invalid publishes left state: %+v", versions)
	}
}
