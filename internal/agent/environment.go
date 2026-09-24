package agent

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
)

const dstGitLabConfigDir = "/run/swarmjet-gitlab"

// DSH removes credential-shaped ambient variables from model-run shell tools.
// Keep GitLab authentication in a per-container tmpfs instead of persisting it
// beside the task logs. The fixed askpass helper contains no credential value.
const dstGitAskpass = `#!/bin/sh
set -eu
case "${1:-}" in
  *Username*|*username*) printf '%s\n' oauth2 ;;
  *Password*|*password*) exec jq -er --arg host "$GITLAB_HOST" '.hosts[$host].token // empty' "$GLAB_CONFIG_DIR/config.yml" ;;
  *) exit 1 ;;
esac
`

const dstGitLabBootstrap = `set -eu
umask 077
python3 - <<'PY'
import json, os
from urllib.parse import urlsplit
host = os.environ['GITLAB_HOST']
scheme = urlsplit(os.environ['GITLAB_URI']).scheme
if scheme not in ('http', 'https'):
    raise SystemExit('invalid GitLab API scheme')
config = {'host': host, 'telemetry': False, 'check_update': False,
          'hosts': {host: {'api_protocol': scheme, 'git_protocol': 'https',
                           'token': os.environ['GITLAB_TOKEN']}}}
with open(os.path.join(os.environ['GLAB_CONFIG_DIR'], 'config.yml'), 'w', encoding='utf-8') as file:
    json.dump(config, file)
PY
unset GITLAB_TOKEN
exec dsh "$@"
`

func prepareDSTGitLab(dir string, env map[string]string) error {
	if env["GITLAB_TOKEN"] == "" || env["GITLAB_URI"] == "" {
		return nil
	}
	path := filepath.Join(dir, "dst-git-askpass.sh")
	if err := atomicWrite(path, []byte(dstGitAskpass)); err != nil {
		return err
	}
	if err := os.Chmod(path, 0700); err != nil {
		return err
	}
	env["GIT_ASKPASS"] = path
	env["GLAB_CONFIG_DIR"] = dstGitLabConfigDir
	return nil
}

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
