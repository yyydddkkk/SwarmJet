// Package omp describes how SwarmJet configures a Cell's oh-my-pi (omp)
// runtime. The platform never writes omp configuration into the workspace: it
// lands in the Cell Home volume under ~/.omp, which the CLI reads and the Agent
// cannot commit. The generated files are overwritten before every Start.
package omp

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

const (
	// ConfigRoot is the omp configuration root inside a Cell (HOME=/cell/home).
	ConfigRoot = "/cell/home/.omp"
	// AgentDir is PI_CODING_AGENT_DIR. Native omp discovery reads config.yml,
	// models.yml, skills/ and rules/ from this directory, so the methodology is
	// delivered below it.
	AgentDir = ConfigRoot + "/methodology"
	// OverlayPath is the platform settings overlay passed with --config. A CLI
	// overlay outranks the repository's project .omp/config.yml, so a repository
	// cannot relax the platform policy.
	OverlayPath = AgentDir + "/platform.yml"
	// ProjectConfigPath is a second overlay that disables capability ids found
	// in the repository's own .omp/tools and .omp/hooks directories. It is
	// passed after OverlayPath so its disabledExtensions array wins.
	ProjectConfigPath = AgentDir + "/project.yml"
	// GlobalConfigPath is the same overlay installed as the global config. It is
	// useful for `omp config` and as a fallback for commands that do not accept
	// --config; the overlay is authoritative during a Cell run.
	GlobalConfigPath = AgentDir + "/config.yml"
	// ModelsPath holds the custom provider that points omp at the platform model
	// proxy. A project cannot override models.yml.
	ModelsPath = AgentDir + "/models.yml"
	// RolePromptPath is appended to the system prompt with
	// --append-system-prompt when the resolved role declares a prompt file.
	RolePromptPath = AgentDir + "/ROLE.md"
	// TokenEnv names the environment variable holding the Agent's model-proxy
	// token. The token itself is never written to disk.
	TokenEnv = "SWARMJET_LLM_TOKEN"
	// ManifestName records the platform-managed paths so the next Start can
	// remove stale files without touching omp's session database.
	ManifestName = ".swarmjet-managed"
)

// ConfigPrefix is the ConfigFiles key prefix for the methodology directory.
const ConfigPrefix = "methodology/"

// Options is everything the platform contributes to an Agent's omp configuration.
type Options struct {
	// Model is the pinned provider/model string (always explicit).
	Model string
	// LLMBaseURL is the platform's model proxy URL (no provider credential).
	LLMBaseURL string
	// MethodologyFiles are delivered verbatim below AgentDir.
	MethodologyFiles []domain.MethodologyFile
	// RolePrompt is the content of the role's prompt file, appended to the
	// system prompt. Empty when the role has no prompt.
	RolePrompt string
	// DenyTools are the role's blocked tool names.
	DenyTools []string
}

// reservedMethodologyPaths cannot be supplied by a methodology version because
// the platform writes them.
var reservedMethodologyPaths = map[string]bool{
	"config.yml":   true,
	"config.yaml":  true,
	"platform.yml": true,
	"models.yml":   true,
	"ROLE.md":      true,
}

// ConfigFiles returns the files to write below ConfigRoot, keyed by relative
// slash-separated path. It always returns the settings overlay and
// models.yml when an LLM base URL is configured.
func ConfigFiles(opts Options) (map[string]string, error) {
	overlay, err := OverlayYAML(opts.DenyTools)
	if err != nil {
		return nil, err
	}
	files := map[string]string{
		ConfigPrefix + "config.yml":   overlay,
		ConfigPrefix + "platform.yml": overlay,
	}
	if opts.LLMBaseURL != "" {
		models, err := ModelsYAML(opts.Model, opts.LLMBaseURL)
		if err != nil {
			return nil, err
		}
		files[ConfigPrefix+"models.yml"] = models
	}
	for _, file := range opts.MethodologyFiles {
		if err := validRelativePath(file.Path); err != nil {
			return nil, fmt.Errorf("methodology file: %w", err)
		}
		if file.Path == ManifestName || reservedMethodologyPaths[file.Path] {
			return nil, fmt.Errorf("methodology file %q is reserved by the platform", file.Path)
		}
		key := ConfigPrefix + file.Path
		if _, exists := files[key]; exists {
			return nil, fmt.Errorf("methodology file %q is defined more than once", file.Path)
		}
		files[key] = file.Content
	}
	if opts.RolePrompt != "" {
		if _, exists := files[ConfigPrefix+"ROLE.md"]; exists {
			return nil, errors.New("methodology file ROLE.md is reserved by the platform")
		}
		files[ConfigPrefix+"ROLE.md"] = opts.RolePrompt
	}
	return files, nil
}

// OverlayYAML renders the platform's omp settings overlay. The Cell container
// is the isolation boundary, so every approval tier is automatic; external
// capabilities that reach the host, a user's accounts or the open internet are
// disabled. Only rules and skills are loaded from the repository.
func OverlayYAML(denyTools []string) (string, error) {
	var b strings.Builder
	b.WriteString("# Generated by SwarmJet before every Cell start. Do not edit.\n")
	b.WriteString("tools:\n")
	b.WriteString("  approvalMode: yolo\n")
	if len(denyTools) > 0 {
		b.WriteString("  approval:\n")
		tools := append([]string(nil), denyTools...)
		sort.Strings(tools)
		seen := map[string]bool{}
		for _, tool := range tools {
			tool = strings.TrimSpace(tool)
			if tool == "" || seen[tool] {
				continue
			}
			seen[tool] = true
			b.WriteString("    " + strconv.Quote(tool) + ": deny\n")
		}
	}
	b.WriteString("extensions: []\n")
	b.WriteString("disabledExtensions:\n")
	for _, id := range disabledExtensions() {
		b.WriteString("  - " + id + "\n")
	}
	b.WriteString("mcp:\n  enableProjectConfig: false\n")
	b.WriteString("browser:\n  enabled: false\n")
	b.WriteString("computer:\n  enabled: false\n")
	b.WriteString("github:\n  enabled: false\n")
	b.WriteString("skills:\n")
	b.WriteString("  enabled: true\n")
	b.WriteString("  enableClaudeUser: false\n")
	b.WriteString("  enableClaudeProject: false\n")
	b.WriteString("  enableCodexUser: false\n")
	b.WriteString("  enableAgentsUser: false\n")
	b.WriteString("  enableAgentsProject: false\n")
	b.WriteString("  enablePiUser: true\n")
	b.WriteString("  enablePiProject: true\n")
	b.WriteString("disabledProviders:\n")
	for _, provider := range disabledProviders() {
		b.WriteString("  - " + provider + "\n")
	}
	return b.String(), nil
}

// disabledExtensions turns off inputs that are not rules or skills. Every
// capability id is exact, so the common context-file names are listed
// explicitly. Extension modules, hooks and commands are additionally disabled
// with --no-extensions, and project MCP servers with mcp.enableProjectConfig.
func disabledExtensions() []string {
	return []string{
		"context-file:project:AGENTS.md",
		"context-file:project:CLAUDE.md",
		"context-file:project:GEMINI.md",
		"context-file:project:copilot-instructions.md",
		"context-file:user:AGENTS.md",
		"context-file:user:CLAUDE.md",
		"context-file:user:GEMINI.md",
		"context-file:user:copilot-instructions.md",
	}
}

// DisabledExtensions returns the fixed platform disable list. The runtime
// combines it with ProjectDisabledExtensions for the repository-specific
// overlay.
func DisabledExtensions() []string { return append([]string(nil), disabledExtensions()...) }

// ProjectDisabledExtensions scans the repository's native .omp/tools and
// .omp/hooks directories and returns the exact capability ids omp would load.
// The runtime writes them to ProjectConfigPath so repository-supplied tools and
// hooks never reach the Agent.
func ProjectDisabledExtensions(workspace string) []string {
	root := filepath.Join(workspace, ".omp")
	seen := map[string]bool{}
	add := func(id string) { seen[id] = true }

	if entries, err := os.ReadDir(filepath.Join(root, "tools")); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if !entry.IsDir() {
				name = strings.TrimSuffix(name, filepath.Ext(name))
			}
			if name != "" {
				add("tool:" + name)
			}
		}
	}
	for _, hookType := range []string{"pre", "post"} {
		entries, err := os.ReadDir(filepath.Join(root, "hooks", hookType))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			name := entry.Name()
			tool := strings.TrimSuffix(name, filepath.Ext(name))
			if tool == "" {
				tool = "*"
			}
			add("hook:" + hookType + ":" + tool + ":" + name)
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ProjectOverlayYAML renders the overlay that replaces disabledExtensions with
// the fixed platform list plus the repository-specific ids.
func ProjectOverlayYAML(projectIDs []string) string {
	all := append(disabledExtensions(), projectIDs...)
	sort.Strings(all)
	var b strings.Builder
	b.WriteString("# Generated by SwarmJet from the repository's .omp inputs. Do not edit.\n")
	b.WriteString("disabledExtensions:\n")
	seen := map[string]bool{}
	for _, id := range all {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		b.WriteString("  - " + id + "\n")
	}
	return b.String()
}

// disabledProviders removes foreign discovery sources and providers that reach
// external accounts. native stays enabled because it reads the delivered
// methodology's rules and skills. Slack is an account-backed integration that
// omp can only supply through a provider or an extension; both are off.
func disabledProviders() []string {
	return []string{
		"agent-plugins",
		"agents",
		"agents-md",
		"claude",
		"claude-md",
		"claude-plugins",
		"cline",
		"codex",
		"cursor",
		"custom-context",
		"github",
		"gemini",
		"mcp-json",
		"omp-plugins",
		"opencode",
		"slack",
		"ssh-json",
		"vscode",
		"windsurf",
	}
}

// ModelsYAML configures a custom provider with omp's pi-native transport. That
// transport forwards the model id unchanged (provider/model) and streams the
// auth gateway's response back, so every upstream provider keeps working
// without the Cell knowing a provider credential. The bearer is read from
// TokenEnv at runtime rather than written into the file.
func ModelsYAML(model, baseURL string) (string, error) {
	provider, modelID, ok := strings.Cut(strings.TrimSpace(model), "/")
	if !ok || provider == "" || modelID == "" {
		return "", fmt.Errorf("omp: model %q must be provider/model", model)
	}
	if strings.ContainsAny(model, "\x00\r\n") || strings.ContainsAny(baseURL, "\x00\r\n") {
		return "", errors.New("omp: model and LLM base URL must not contain control characters")
	}
	var b strings.Builder
	b.WriteString("# Generated by SwarmJet before every Cell start. Do not edit.\n")
	b.WriteString("providers:\n")
	b.WriteString("  " + strconv.Quote(provider) + ":\n")
	b.WriteString("    baseUrl: " + strconv.Quote(strings.TrimRight(baseURL, "/")) + "\n")
	b.WriteString("    transport: pi-native\n")
	// api is required by omp's models.yml schema even though pi-native owns the
	// wire format. openai-completions is the neutral chat shape.
	b.WriteString("    api: openai-completions\n")
	b.WriteString("    apiKey: " + TokenEnv + "\n")
	b.WriteString("    models:\n")
	b.WriteString("      - id: " + strconv.Quote(modelID) + "\n")
	b.WriteString("        name: " + strconv.Quote(model) + "\n")
	return b.String(), nil
}

// LaunchArgs is the in-container argv for a Cell's omp process. The overlay is
// a CLI config overlay, so it wins over the repository's project settings. The
// pinned model is passed explicitly so a project modelRole cannot change it.
func LaunchArgs(model string, hasRolePrompt bool) []string {
	args := []string{"omp", "acp", "--no-extensions", "--config", OverlayPath, "--config", ProjectConfigPath}
	if model != "" {
		args = append(args, "--model", model)
	}
	if hasRolePrompt {
		args = append(args, "--append-system-prompt", RolePromptPath)
	}
	return args
}

// ValidatePath reports whether p is a safe ConfigFiles key.
func ValidatePath(p string) error { return validRelativePath(p) }

// validRelativePath accepts a clean relative slash path and rejects anything
// that could escape ConfigRoot or collide with a reserved file.
func validRelativePath(p string) error {
	if p == "" {
		return errors.New("empty path")
	}
	if strings.ContainsRune(p, 0) || strings.ContainsAny(p, "\\\r\n") {
		return fmt.Errorf("invalid path %q", p)
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") || p == ".." {
		return fmt.Errorf("path %q must stay below the configuration directory", p)
	}
	clean := path.Clean(p)
	if clean != p || clean == "." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path %q must be a clean relative path", p)
	}
	return nil
}
