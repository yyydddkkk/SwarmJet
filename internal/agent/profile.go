package agent

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const profileFileLimit = 1 << 20

func roleScope(role string) string {
	if role == "merge" {
		return "review"
	}
	return role
}

// readProfileFile refuses symlinks in every component, including during path
// replacement races. An absent file is distinct from an unreadable credential.
func readProfileFile(path string) ([]byte, bool, error) {
	if err := safeDirectory(filepath.Dir(path), false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, errors.New("private profile directory is unsafe or unreadable")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, false, errors.New("invalid private profile path")
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, false, errors.New("unable to open private profile root")
	}
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	for i, part := range parts {
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		}
		next, openErr := syscall.Openat(fd, part, flags, 0)
		syscall.Close(fd)
		if openErr != nil {
			if errors.Is(openErr, syscall.ENOENT) {
				return nil, false, nil
			}
			return nil, false, errors.New("private profile file is unsafe or unreadable")
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), abs)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, errors.New("private profile file must be regular")
	}
	if info.Size() > profileFileLimit {
		return nil, false, errors.New("private profile file exceeds 1 MiB limit")
	}
	data, err := io.ReadAll(io.LimitReader(f, profileFileLimit+1))
	if err != nil {
		return nil, false, errors.New("unable to read private profile file")
	}
	if len(data) > profileFileLimit {
		return nil, false, errors.New("private profile file exceeds 1 MiB limit")
	}
	return data, true, nil
}

// prepareProfile snapshots only the OpenCode auth and JSON configuration files.
// Sessions, databases, executables, and the master home are never copied/mounted.
func (r *Runtime) prepareProfile(taskID, role, executionID string) (home, config, authPath, snapshotPath string, hasAuth bool, err error) {
	scope := roleScope(role)
	home = filepath.Join(r.DataDir, "opencode", taskID, scope)
	config = filepath.Join(r.DataDir, "opencode", taskID, scope+"-config")
	authPath = filepath.Join(home, "opencode", "auth.json")
	if err = safeDirectory(filepath.Dir(authPath), true); err != nil {
		return
	}
	if err = safeDirectory(filepath.Join(config, "opencode"), true); err != nil {
		return
	}
	var auth []byte
	auth, hasAuth, err = readProfileFile(authPath)
	if err != nil {
		return
	}
	master := filepath.Join(r.DataDir, "runtime-profiles", "default", "home")
	if !hasAuth {
		auth, hasAuth, err = readProfileFile(filepath.Join(master, ".local", "share", "opencode", "auth.json"))
		if err != nil {
			return
		}
		if hasAuth {
			if _, err = authSecrets(auth); err != nil {
				return
			}
			err = atomicWrite(authPath, auth)
			if err != nil {
				return
			}
		}
	}
	if hasAuth {
		if _, err = authSecrets(auth); err != nil {
			return
		}
		// Retain the execution's initial auth for redaction after provider refresh.
		snapshotPath = filepath.Join(r.DataDir, "opencode", taskID, scope+"-auth-snapshots", executionID+".json")
		if err = safeDirectory(filepath.Dir(snapshotPath), true); err != nil {
			return
		}
		var present bool
		_, present, err = readProfileFile(snapshotPath)
		if err != nil {
			return
		}
		if !present {
			if err = atomicWrite(snapshotPath, auth); err != nil {
				return
			}
		}
	}
	err = seedRoleConfig(config, filepath.Join(master, ".config", "opencode"), filepath.Join(r.DataDir, "opencode-config"))
	return
}

func seedRoleConfig(destination, profile, legacy string) error {
	marker := filepath.Join(destination, ".profile-initialized")
	if _, exists, err := readProfileFile(marker); err != nil {
		return err
	} else if exists {
		return nil
	}
	names := []string{"opencode.json", "opencode.jsonc"}
	existing := false
	for _, name := range names {
		if _, present, err := readProfileFile(filepath.Join(destination, "opencode", name)); err != nil {
			return err
		} else {
			existing = existing || present
		}
	}
	if existing {
		return atomicWrite(marker, []byte("configuration snapshot\n"))
	}
	files := map[string][]byte{}
	for _, name := range names {
		data, present, err := readProfileFile(filepath.Join(profile, name))
		if err != nil {
			return err
		}
		if present {
			files[name] = data
		}
	}
	if len(files) == 0 {
		for _, name := range names {
			data, present, err := readProfileFile(filepath.Join(legacy, "opencode", name))
			if err != nil {
				return err
			}
			if !present {
				data, present, err = readProfileFile(filepath.Join(legacy, name))
				if err != nil {
					return err
				}
			}
			if present {
				files[name] = data
			}
		}
	}
	if len(files) == 0 {
		return nil
	} // A later login/configuration may seed an empty role.
	// Publish the pair together; an interrupted copy must not freeze half a profile.
	staging, err := os.MkdirTemp(destination, ".profile-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for _, name := range names {
		if data, ok := files[name]; ok {
			if err = atomicWrite(filepath.Join(staging, name), data); err != nil {
				return err
			}
		}
	}
	if err = syscall.Rename(staging, filepath.Join(destination, "opencode")); err != nil {
		return err
	}
	return atomicWrite(marker, []byte("configuration snapshot\n"))
}

func authSecrets(data []byte) ([]string, error) {
	var document map[string]any
	if json.Unmarshal(data, &document) != nil || document == nil {
		return nil, errors.New("OpenCode auth must be a valid JSON object for safe redaction")
	}
	found := map[string]bool{}
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
				switch normalized {
				case "key", "apikey", "access", "accesstoken", "refresh", "refreshtoken", "token", "secret", "clientsecret", "idtoken", "openaiapikey":
					if text, ok := item.(string); ok && text != "" {
						found[text] = true
					}
				}
				walk(item)
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(document)
	out := make([]string, 0, len(found))
	for secret := range found {
		out = append(out, secret)
	}
	return out, nil
}
func (r *Runtime) profileSecrets(rec record) ([]string, error) {
	if rec.ProfileHome != "" {
		return r.cliProfileSecrets(rec)
	}
	authPath := rec.AuthPath
	if authPath == "" {
		authPath = filepath.Join(r.DataDir, "opencode", rec.TaskID, roleScope(rec.Role), "opencode", "auth.json")
	}
	out := []string{}
	for _, item := range []struct {
		path     string
		required bool
	}{{authPath, rec.AuthRequired}, {rec.AuthSnapshotPath, rec.AuthSnapshotPath != ""}} {
		if item.path == "" {
			continue
		}
		data, present, err := readProfileFile(item.path)
		if err != nil {
			return nil, errors.New("cannot safely read OpenCode credentials for output redaction")
		}
		if !present {
			if item.required {
				return nil, errors.New("OpenCode credentials required for output redaction are missing")
			}
			continue
		}
		secrets, err := authSecrets(data)
		if err != nil {
			return nil, err
		}
		out = append(out, secrets...)
	}
	return out, nil
}
