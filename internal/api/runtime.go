package api

import "net/http"

// runtime is the entry point for the CLI login terminal under /api/runtime/*.
// The terminal is wired into serve by the connect task; until then the console
// reports that CLI login is not configured instead of proxying to a process
// that no longer exists.
func (a *server) runtime(w http.ResponseWriter, r *http.Request) {
	problem(w, http.StatusServiceUnavailable, "CLI login is not configured")
}
