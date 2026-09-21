package agent

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
)

// Launch files belong on the execution host, beside the other private artifacts.
// Copy the environment so preparing an execution cannot mutate the caller's map.
func prepareEnvironment(dir string, requested map[string]string) (map[string]string, error) {
	env := maps.Clone(requested)
	if env == nil {
		env = map[string]string{}
	}
	if env["GITLAB_URI"] == "" {
		return env, nil
	}
	u, err := url.Parse(env["GITLAB_URI"])
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, errors.New("invalid GitLab URL for agent environment")
	}
	configDir := filepath.Join(dir, "glab")
	if err = safeDirectory(configDir, true); err != nil {
		return nil, err
	}
	content := fmt.Sprintf("host: %q\ntelemetry: false\nhosts:\n  %q:\n    api_protocol: %q\n    git_protocol: https\n", u.Host, u.Host, u.Scheme)
	if err = atomicWrite(filepath.Join(configDir, "config.yml"), []byte(content)); err != nil {
		return nil, err
	}
	env["GITLAB_HOST"] = u.Host
	env["GIT_ASKPASS"] = "/usr/local/bin/swarmjet-askpass"
	env["GIT_TERMINAL_PROMPT"] = "0"
	env["GLAB_CONFIG_DIR"] = configDir
	return env, nil
}
