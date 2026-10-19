package main

import (
	"net/http"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/runtimeadmin"
)

// newCLILoginHandler builds the in-process CLI login terminal. It is available
// only when Docker is reachable: the CLI login container is a real sibling
// container, and the base compose file intentionally does not mount the socket.
// The returned reason is shown by /api/runtime/* when the handler is nil.
func newCLILoginHandler(dataDir string) (http.Handler, string) {
	if !runtimeadmin.DockerAvailable() {
		return nil, "CLI 登录需要 Docker：请叠加 deploy/compose.cells.yaml 后重启，使 serve 能访问 Docker socket"
	}
	return runtimeadmin.NewInProcess(runtimeadmin.Config{
		DataDir: dataDir,
		Image:   env("AGENT_IMAGE", "swarmjet-agent:local"),
	}), ""
}

// publicMCPURL is advertised in generated local Agent configurations. The
// console and the MCP endpoint share PUBLIC_ORIGIN.
func publicMCPURL() string {
	origin := strings.TrimRight(env("PUBLIC_ORIGIN", "http://localhost:8180"), "/")
	return origin + "/mcp"
}

// cliLoginStatus reports the console setup page state. The handler is nil when
// Docker is unavailable; the login state is read from the same profile the
// terminal writes on every call, so it reflects a login without a restart.
func cliLoginStatus(handler http.Handler, dataDir string) func() (bool, bool) {
	return func() (bool, bool) {
		if handler == nil {
			return false, false
		}
		return true, runtimeadmin.LoggedIn(dataDir)
	}
}
