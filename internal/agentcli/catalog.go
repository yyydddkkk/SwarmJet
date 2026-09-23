// Package agentcli describes the supported CLIs without depending on orchestration.
package agentcli

type Spec struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Executable   string `json:"executable"`
	SetupCommand string `json:"setupCommand"`
	// Files is an allowlist relative to the configuration HOME. Never copy sessions.
	Files []string `json:"-"`
}

func Catalog() []Spec {
	return []Spec{
		{"opencode", "OpenCode", "opencode", "opencode auth login", []string{".local/share/opencode/auth.json", ".local/state/opencode/model.json", ".config/opencode/opencode.json", ".config/opencode/opencode.jsonc", "opencode.json", "opencode.jsonc"}},
		{"codex", "Codex", "codex", "codex login --device-auth", []string{".codex/auth.json", ".codex/config.toml"}},
		{"dst", "DeepSeek Harness TUI", "dsh", "dst", []string{".dsh/settings.yaml", ".dsh/.credentials.yaml", ".dsh/.env"}},
		{"agy", "Antigravity", "agy", "agy", []string{".gemini/antigravity-cli/settings.json", ".gemini/config/config.json", ".config/gcloud/application_default_credentials.json"}},
		{"claude", "Claude Code", "claude", "claude", []string{".claude/settings.json", ".claude/.credentials.json", ".claude.json"}},
	}
}

func Lookup(id string) (Spec, bool) {
	for _, spec := range Catalog() {
		if spec.ID == id {
			return spec, true
		}
	}
	return Spec{}, false
}
