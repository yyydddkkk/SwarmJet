package omp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestOverlayDisablesProjectInputsAndExternalCapabilities(t *testing.T) {
	out, err := OverlayYAML([]string{"edit", "write", "edit"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"approvalMode: yolo",
		`"edit": deny`,
		`"write": deny`,
		"extensions: []",
		"mcp:\n  enableProjectConfig: false",
		"browser:\n  enabled: false",
		"computer:\n  enabled: false",
		"github:\n  enabled: false",
		"context-file:project:AGENTS.md",
		"context-file:project:CLAUDE.md",
		"  - slack\n",
		"  - github\n",
		"  - omp-plugins\n",
		"  enablePiProject: true",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("overlay does not contain %q:\n%s", want, out)
		}
	}
	if strings.Count(out, `"edit": deny`) != 1 {
		t.Errorf("duplicate denyTools were not deduplicated:\n%s", out)
	}
}

func TestModelsYAMLPointsAtThePlatformProxy(t *testing.T) {
	out, err := ModelsYAML("deepseek/deepseek-chat", "http://swarmjet:8080/llm/")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`  "deepseek":`,
		`    baseUrl: "http://swarmjet:8080/llm"`,
		"    transport: pi-native",
		"    apiKey: " + TokenEnv,
		`      - id: "deepseek-chat"`,
		`        name: "deepseek/deepseek-chat"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("models.yml does not contain %q:\n%s", want, out)
		}
	}
	if _, err := ModelsYAML("deepseek", "http://proxy/llm"); err == nil {
		t.Fatal("a model without provider/model was accepted")
	}
}

func TestConfigFilesLayoutAndReservedPaths(t *testing.T) {
	files, err := ConfigFiles(Options{
		Model:      "openrouter/anthropic/claude-sonnet",
		LLMBaseURL: "http://proxy/llm",
		RolePrompt: "You verify changes.",
		DenyTools:  []string{"bash"},
		MethodologyFiles: []domain.MethodologyFile{
			{Path: "methodology.yaml", Content: "defaultRole: worker\n"},
			{Path: "skills/review/SKILL.md", Content: "# review\n"},
			{Path: "rules/review.md", Content: "review everything\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"methodology/config.yml",
		"methodology/platform.yml",
		"methodology/models.yml",
		"methodology/methodology.yaml",
		"methodology/skills/review/SKILL.md",
		"methodology/rules/review.md",
		"methodology/ROLE.md",
	} {
		if _, ok := files[key]; !ok {
			t.Errorf("ConfigFiles is missing %q", key)
		}
	}
	if files["methodology/ROLE.md"] != "You verify changes." {
		t.Errorf("role prompt = %q", files["methodology/ROLE.md"])
	}
	if !strings.Contains(files["methodology/models.yml"], `"openrouter":`) ||
		!strings.Contains(files["methodology/models.yml"], `"anthropic/claude-sonnet"`) {
		t.Errorf("provider/model was not split at the first slash:\n%s", files["methodology/models.yml"])
	}

	for _, path := range []string{"../escape", "/absolute", "a/../../b", "config.yml", "models.yml"} {
		if _, err := ConfigFiles(Options{Model: "p/m", MethodologyFiles: []domain.MethodologyFile{{Path: path, Content: "x"}}}); err == nil {
			t.Errorf("methodology path %q was accepted", path)
		}
	}
	if _, err := ConfigFiles(Options{Model: "p/m", MethodologyFiles: []domain.MethodologyFile{
		{Path: "skills/a/SKILL.md", Content: "a"},
		{Path: "skills/a/SKILL.md", Content: "b"},
	}}); err == nil {
		t.Error("duplicate methodology path was accepted")
	}
}

func TestLaunchArgsUseTheOverlay(t *testing.T) {
	args := LaunchArgs("deepseek/deepseek-chat", false)
	want := []string{"omp", "acp", "--no-extensions", "--config", OverlayPath, "--config", ProjectConfigPath, "--model", "deepseek/deepseek-chat"}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("args = %v, want %v", args, want)
	}
	args = LaunchArgs("deepseek/deepseek-chat", true)
	if !contains(args, "--append-system-prompt") || !contains(args, RolePromptPath) {
		t.Fatalf("role prompt args = %v", args)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestProjectDisabledExtensionsCoversRepoToolsAndHooks(t *testing.T) {
	workspace := t.TempDir()
	for _, path := range []string{
		".omp/tools/foo.ts",
		".omp/tools/bar/index.ts",
		".omp/hooks/pre/security.sh",
		".omp/hooks/post/*.ts",
	} {
		full := filepath.Join(workspace, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ids := ProjectDisabledExtensions(workspace)
	want := []string{
		"hook:post:*:*.ts",
		"hook:pre:security:security.sh",
		"tool:bar",
		"tool:foo",
	}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	overlay := ProjectOverlayYAML(ids)
	for _, id := range append(DisabledExtensions(), ids...) {
		if !strings.Contains(overlay, "  - "+id+"\n") {
			t.Errorf("project overlay is missing %q:\n%s", id, overlay)
		}
	}
}
