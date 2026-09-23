package runtimeadmin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

// ProfileDirs is the only persistent host directory exposed to the setup container.
type ProfileDirs struct{ Home, OpenCodeData, OpenCodeConfig, CodexHome string }

func GetProfileDirs(dataDir string) ProfileDirs {
	abs, _ := filepath.Abs(dataDir)
	home := filepath.Join(abs, "runtime-profiles", "default", "home")
	return ProfileDirs{Home: home, OpenCodeData: filepath.Join(home, ".local", "share", "opencode"), OpenCodeConfig: filepath.Join(home, ".config", "opencode"), CodexHome: filepath.Join(home, ".codex")}
}

const maxOutput = 2 << 20
const maxInlineConfig = 64 << 10

var providerID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var providerEnv = map[string]string{"OPENAI_API_KEY": "openai", "CODEX_API_KEY": "openai", "CLAUDE_CODE_OAUTH_TOKEN": "anthropic", "AGY_ADC_AUTH": "google", "GOOGLE_GEMINI_BASE_URL": "google", "ANTHROPIC_API_KEY": "anthropic", "GOOGLE_GENERATIVE_AI_API_KEY": "google", "GEMINI_API_KEY": "google", "DEEPSEEK_API_KEY": "deepseek", "OPENROUTER_API_KEY": "openrouter", "GROQ_API_KEY": "groq", "XAI_API_KEY": "xai", "MISTRAL_API_KEY": "mistral", "AZURE_OPENAI_API_KEY": "azure"}

// openat with O_NOFOLLOW on every component prevents credential paths from
// traversing symlinks created through the administrative terminal.
func readProfileFile(root, rel string) ([]byte, error) {
	if filepath.IsAbs(rel) || filepath.Clean(rel) != rel || strings.HasPrefix(rel, "../") {
		return nil, errors.New("unsafe profile path")
	}
	fd, e := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if e != nil {
		return nil, e
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		}
		next, err := syscall.Openat(fd, part, flags, 0)
		syscall.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), "profile")
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("profile file is not regular")
	}
	b, e := io.ReadAll(io.LimitReader(f, maxOutput+1))
	if e != nil {
		return nil, e
	}
	if len(b) > maxOutput {
		return nil, errors.New("profile file exceeds size limit")
	}
	return b, nil
}
func validateProfileHome(home string) error {
	for p := home; p != filepath.Dir(p); p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return errors.New("runtime profile path contains a symlink")
		}
	}
	return nil
}
func ensureProfileHome(home string) error {
	if e := validateProfileHome(home); e != nil {
		return e
	}
	return os.MkdirAll(home, 0700)
}

type profileMetadata struct {
	Providers                     []string
	ConfigExists, CodexConfigured bool
	Warnings                      []string
}

func profileInfo(dirs ProfileDirs, env map[string]string) profileMetadata {
	out := profileMetadata{Providers: []string{}, Warnings: []string{}}
	providers := map[string]bool{}
	for key, value := range env {
		if p, ok := providerEnv[key]; ok && value != "" {
			providers[p] = true
		}
	}
	if inline := env["OPENCODE_CONFIG_CONTENT"]; inline != "" {
		if len(inline) > maxInlineConfig {
			out.Warnings = append(out.Warnings, "部署提供的 OpenCode 配置过大，已忽略。")
		} else {
			var config struct {
				Provider map[string]json.RawMessage `json:"provider"`
			}
			clean, e := jsonc([]byte(inline))
			if e == nil {
				e = json.Unmarshal(clean, &config)
			}
			if e != nil {
				out.Warnings = append(out.Warnings, "无法解析部署提供的 OpenCode 配置。")
			} else {
				out.ConfigExists = true
				for key := range config.Provider {
					if providerID.MatchString(key) {
						providers[key] = true
					}
				}
			}
		}
	}
	if e := validateProfileHome(dirs.Home); e != nil {
		for key := range providers {
			out.Providers = append(out.Providers, key)
		}
		sort.Strings(out.Providers)
		out.Warnings = append(out.Warnings, "运行环境配置目录未通过安全检查，无法读取。")
		return out
	}
	auth, e := readProfileFile(dirs.Home, ".local/share/opencode/auth.json")
	if e == nil {
		var keys map[string]json.RawMessage
		if json.Unmarshal(auth, &keys) == nil {
			for key := range keys {
				if providerID.MatchString(key) {
					providers[key] = true
				}
			}
		} else {
			out.Warnings = append(out.Warnings, "OpenCode 认证文件不是有效的 JSON。")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		out.Warnings = append(out.Warnings, "OpenCode 认证文件未通过安全检查，无法读取。")
	}
	for _, name := range []string{".config/opencode/opencode.json", ".config/opencode/opencode.jsonc", ".config/opencode/config.json", ".config/opencode/config.jsonc", "opencode.json", "opencode.jsonc"} {
		b, e := readProfileFile(dirs.Home, name)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			out.Warnings = append(out.Warnings, "OpenCode 配置文件未通过安全检查，无法读取。")
			continue
		}
		out.ConfigExists = true
		var config struct {
			Provider map[string]json.RawMessage `json:"provider"`
		}
		clean, e := jsonc(b)
		if e == nil {
			e = json.Unmarshal(clean, &config)
		}
		if e != nil {
			out.Warnings = append(out.Warnings, "无法解析 OpenCode 配置文件。")
			continue
		}
		for key := range config.Provider {
			if providerID.MatchString(key) {
				providers[key] = true
			}
		}
	}
	b, e := readProfileFile(dirs.Home, ".codex/auth.json")
	if e == nil {
		var keys map[string]json.RawMessage
		if json.Unmarshal(b, &keys) == nil && len(keys) > 0 {
			out.CodexConfigured = true
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		out.Warnings = append(out.Warnings, "Codex 认证文件未通过安全检查，无法读取。")
	}
	for key := range providers {
		out.Providers = append(out.Providers, key)
	}
	sort.Strings(out.Providers)
	return out
}

// jsonc strips comments and trailing commas while preserving string contents.
func jsonc(input []byte) ([]byte, error) {
	out := make([]byte, 0, len(input))
	quoted, escaped := false, false
	for i := 0; i < len(input); i++ {
		c := input[i]
		if quoted {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(input) && input[i+1] == '/' {
			i += 2
			for i < len(input) && input[i] != '\n' {
				i++
			}
			out = append(out, '\n')
			continue
		}
		if c == '/' && i+1 < len(input) && input[i+1] == '*' {
			i += 2
			closed := false
			for i+1 < len(input) {
				if input[i] == '*' && input[i+1] == '/' {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated JSONC comment")
			}
			out = append(out, ' ')
			continue
		}
		out = append(out, c)
	}
	clean := make([]byte, 0, len(out))
	quoted, escaped = false, false
	for i, c := range out {
		if quoted {
			clean = append(clean, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
		}
		if c == ',' {
			j := i + 1
			for j < len(out) && strings.ContainsRune(" \t\r\n", rune(out[j])) {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				continue
			}
		}
		clean = append(clean, c)
	}
	return clean, nil
}
