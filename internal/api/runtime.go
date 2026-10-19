package api

import (
	"net/http"
	"strings"
)

// runtime serves the CLI login terminal and model catalog from the runtimeadmin
// handler mounted in this same process. The console session authenticates the
// caller; Docker never leaves serve.
func (a *server) runtime(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/runtime")
	switch path {
	case "/status", "/models", "/terminal":
	default:
		problem(w, http.StatusNotFound, "runtime route not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.cfg.CLILogin == nil {
		reason := a.cfg.CLILoginReason
		if reason == "" {
			reason = "CLI login is not configured; enable Cells (deploy/compose.cells.yaml) so serve can reach Docker"
		}
		problem(w, http.StatusServiceUnavailable, reason)
		return
	}
	if path == "/terminal" {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			problem(w, http.StatusBadRequest, "websocket upgrade required")
			return
		}
		originCheck := r.Clone(r.Context())
		originCheck.Header = r.Header.Clone()
		originCheck.Header.Set("X-Requested-With", "swarmjet")
		if !csrfAllowed(originCheck, a.cfg.AllowedOrigin) {
			problem(w, http.StatusForbidden, "same-origin terminal required")
			return
		}
	}
	http.StripPrefix("/api/runtime", a.cfg.CLILogin).ServeHTTP(w, r)
}
