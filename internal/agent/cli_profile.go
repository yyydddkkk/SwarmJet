package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
	"swarmjet/internal/domain"
)

// publishProfile copies only the adapter's allowlisted files, atomically. An
// empty snapshot is still a snapshot: later roles must not adopt newer defaults.
func publishProfile(destination, source string, files []string, extra map[string][]byte) error {
	if _, exists, err := readProfileFile(filepath.Join(destination, ".swarmjet-profile")); err != nil {
		return err
	} else if exists {
		return nil
	}
	if err := safeDirectory(filepath.Dir(destination), true); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".profile-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	data := map[string][]byte{}
	for _, name := range files {
		b, present, err := readProfileFile(filepath.Join(source, name))
		if err != nil {
			return fmt.Errorf("cannot snapshot Agent configuration %s: %w", name, err)
		}
		if present {
			data[name] = b
		}
	}
	for name, b := range extra {
		data[name] = b
	}
	for name, b := range data {
		path := filepath.Join(stage, name)
		if err := safeDirectory(filepath.Dir(path), true); err != nil {
			return err
		}
		if err := atomicWrite(path, b); err != nil {
			return err
		}
	}
	if err := atomicWrite(filepath.Join(stage, ".swarmjet-profile"), []byte("1\n")); err != nil {
		return err
	}
	return os.Rename(stage, destination)
}

func (r *Runtime) prepareCLIProfile(req domain.AgentRequest, a cliAdapter) (string, string, error) {
	root := filepath.Join(r.DataDir, "agent-profiles", req.TaskID, a.ID)
	seed := filepath.Join(root, "snapshot")
	home := filepath.Join(root, roleScope(req.Role))
	master := filepath.Join(r.DataDir, "runtime-profiles/default/home")
	// Inline OpenCode settings can include a default model; freeze them with files.
	extra := map[string][]byte{}
	if a.ID == "opencode" && req.Env["OPENCODE_CONFIG_CONTENT"] != "" {
		extra[".inline-opencode.json"] = []byte(req.Env["OPENCODE_CONFIG_CONTENT"])
	}
	if err := publishProfile(seed, master, a.Files, extra); err != nil {
		return "", "", err
	}
	files := append(append([]string{}, a.Files...), ".inline-opencode.json")
	if err := publishProfile(home, seed, files, nil); err != nil {
		return "", "", err
	}
	// Required home subdirectories are private, including when login used env keys.
	for _, name := range a.Files {
		if err := safeDirectory(filepath.Dir(filepath.Join(home, name)), true); err != nil {
			return "", "", err
		}
	}
	if a.ID == "opencode" {
		delete(req.Env, "OPENCODE_CONFIG_CONTENT")
		if b, exists, err := readProfileFile(filepath.Join(seed, ".inline-opencode.json")); err != nil {
			return "", "", err
		} else if exists {
			req.Env["OPENCODE_CONFIG_CONTENT"] = string(b)
		}
	}
	return home, seed, nil
}

// Configuration parsers differ (JSONC, TOML, YAML, dotenv). Walk structured
// values where possible, with a quoted-assignment fallback for JSONC/TOML.
var quotedProfileValue = regexp.MustCompile(`(?m)([A-Za-z0-9_.-]+|"[A-Za-z0-9_.-]+")\s*[:=]\s*("(?:[^"\\]|\\.)*"|'[^']*')`)

func secretProfileKey(key string) bool {
	key = strings.ToLower(key)
	for _, part := range []string{"key", "token", "secret", "password", "credential", "authorization"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return key == "access" || key == "refresh"
}

func fileSecrets(name string, data []byte) []string {
	values := []string{}
	credentials := strings.Contains(name, "auth.json") || strings.Contains(name, "credentials")
	var document any
	if yaml.Unmarshal(data, &document) == nil {
		var walk func(any, bool)
		walk = func(v any, sensitive bool) {
			switch x := v.(type) {
			case string:
				if sensitive && x != "" {
					values = append(values, x)
				}
			case map[string]any:
				for key, item := range x {
					walk(item, sensitive || secretProfileKey(key))
				}
			case map[any]any:
				for key, item := range x {
					walk(item, sensitive || secretProfileKey(fmt.Sprint(key)))
				}
			case []any:
				for _, item := range x {
					walk(item, sensitive)
				}
			}
		}
		walk(document, credentials)
	}
	for _, match := range quotedProfileValue.FindAllSubmatch(data, -1) {
		if !credentials && !secretProfileKey(string(match[1])) {
			continue
		}
		quoted := match[2]
		var value string
		if quoted[0] == '"' {
			_ = json.Unmarshal(quoted, &value)
		} else {
			value = string(quoted[1 : len(quoted)-1])
		}
		if value != "" {
			values = append(values, value)
		}
	}
	if strings.HasSuffix(name, ".env") {
		for _, line := range strings.Split(string(data), "\n") {
			if key, v, ok := strings.Cut(line, "="); ok && secretProfileKey(key) {
				v = strings.TrimSpace(v)
				if strings.HasPrefix(v, "\"") || strings.HasPrefix(v, "'") {
					continue
				} // quoted values were collected above
				v, _, _ = strings.Cut(v, "#")
				v = strings.TrimSpace(v)
				if v != "" {
					values = append(values, v)
				}
			}
		}
	}
	return values
}

func (r *Runtime) cliProfileSecrets(rec record) ([]string, error) {
	a, err := adapterFor(rec.Adapter)
	if err != nil {
		return nil, err
	}
	values := []string{}
	for _, root := range []string{rec.ProfileHome, rec.ProfileSnapshot} {
		if root == "" {
			return nil, errors.New("Agent profile snapshot missing")
		}
		if _, exists, err := readProfileFile(filepath.Join(root, ".swarmjet-profile")); err != nil || !exists {
			return nil, errors.New("Agent profile snapshot unsafe or missing")
		}
		for _, name := range append(a.Files, ".inline-opencode.json") {
			b, _, err := readProfileFile(filepath.Join(root, name))
			if err != nil {
				return nil, errors.New("cannot safely read Agent credentials for output redaction")
			}
			values = append(values, fileSecrets(name, b)...)
		}
	}
	return values, nil
}
