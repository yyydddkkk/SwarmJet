package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestMethodologyTemplatesListAndInstallPermissions(t *testing.T) {
	env := newAgentScopeEnv(t)
	for _, token := range []string{env.userToken, env.adminToken, env.queenToken, env.foragerToken} {
		templates := decode[listMethodologyTemplatesOutput](t, callTool(t, env.connect(token), "list_methodology_templates", map[string]any{}))
		if len(templates.Templates) != 2 {
			t.Fatalf("templates = %+v", templates)
		}
	}
	for i, token := range []string{env.userToken, env.queenToken, env.adminToken} {
		out := decode[publishMethodologyOutput](t, callTool(t, env.connect(token), "install_methodology_template", map[string]any{"hiveId": env.hive.ID, "name": "minimal", "note": "chosen by user"}))
		m := out.Methodology
		if m.Version != i+1 || !m.Active || m.Builtin || m.Note != "chosen by user" || m.Source == nil || m.Source.URL != "builtin:minimal" || m.Source.Ref == "" {
			t.Fatalf("installed = %+v", m)
		}
		if i == 1 && m.CreatedBy != domain.AddressQueen {
			t.Fatalf("Queen publisher = %s", m.CreatedBy)
		}
	}
	// Even a role allowed to publish arbitrary methodologies cannot install templates.
	callTool(t, env.connect(env.userToken), "publish_methodology", map[string]any{"hiveId": env.hive.ID, "manifest": methodologyYAML("publisher", true)})
	res := callTool(t, env.connect(env.foragerToken), "install_methodology_template", map[string]any{"hiveId": env.hive.ID, "name": "minimal"})
	if !res.IsError || !strings.Contains(toolErrorText(res), "permission denied") {
		t.Fatalf("ordinary agent install = %+v", res)
	}
	res = callTool(t, env.connect(env.queenToken), "install_methodology_template", map[string]any{"hiveId": env.otherHive.ID, "name": "minimal"})
	if !res.IsError || !strings.Contains(toolErrorText(res), "can only access Hive") {
		t.Fatalf("cross-Hive install = %+v", res)
	}
	for _, args := range []map[string]any{
		{"hiveId": env.hive.ID, "name": "missing"},
		{"hiveId": env.hive.ID, "name": "minimal", "note": strings.Repeat("x", maxMethodologyNote+1)},
	} {
		res = callTool(t, env.connect(env.userToken), "install_methodology_template", args)
		if !res.IsError {
			t.Fatalf("invalid install = %+v", res)
		}
	}
	versions, err := env.backend.MethodologyVersions(context.Background(), env.hive.ID)
	if err != nil || len(versions) != 4 {
		t.Fatalf("versions = %+v, %v", versions, err)
	}
}
