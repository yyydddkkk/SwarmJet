package runtimeadmin

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// openCodeAuthFile is the OpenCode login state written by `opencode auth
// login`. Only this file is handed to Cells; the rest of the setup profile
// (sessions, history, other CLI credentials) stays private.
const openCodeAuthFile = ".local/share/opencode/auth.json"

// OpenCodeAuth returns the OpenCode login state for the shared CLI profile. It
// uses the same O_NOFOLLOW-safe reader as status and never logs the content.
// The returned bytes are a credential: callers must not write them to disk,
// logs or API responses.
func OpenCodeAuth(dataDir string) ([]byte, error) {
	dirs := GetProfileDirs(dataDir)
	body, err := readProfileFile(dirs.Home, openCodeAuthFile)
	if err != nil {
		return nil, err
	}
	var providers map[string]json.RawMessage
	if err = json.Unmarshal(body, &providers); err != nil {
		return nil, errors.New("opencode auth state is not valid JSON")
	}
	if len(providers) == 0 {
		// An empty object is a logged-out state, not a usable credential.
		return nil, os.ErrNotExist
	}
	return body, nil
}

// LoggedIn reports whether the CLI login terminal has produced an OpenCode
// login state. It never returns or logs the credential itself.
func LoggedIn(dataDir string) bool {
	_, err := OpenCodeAuth(dataDir)
	return err == nil
}

// DockerAvailable reports whether this process can reach a Docker daemon. The
// base compose file deliberately does not mount the socket; only the Cells
// overlay makes the CLI login terminal available.
func DockerAvailable() bool {
	if host := strings.TrimSpace(os.Getenv("DOCKER_HOST")); host != "" {
		if !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "unix:") {
			// A TCP or SSH endpoint does not depend on a local socket.
			return true
		}
		host = strings.TrimPrefix(strings.TrimPrefix(host, "unix://"), "unix:")
		return isDockerSocket(host)
	}
	return isDockerSocket("/var/run/docker.sock")
}

func isDockerSocket(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}
