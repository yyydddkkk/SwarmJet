package api

import (
	"net/http"
	"strings"
	"time"
	"unicode"

	"swarmjet/internal/domain"
)

// nodeHeartbeat lets an execution node register or refresh itself. It uses the
// shared node secret instead of a console session and is reached before the
// browser CSRF check because nodes are not browsers.
func (a *server) nodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	if a.s == nil {
		problem(w, 503, "execution node registration is unavailable")
		return
	}
	if a.cfg.NodeSecret == "" {
		problem(w, 503, "execution node registration is not enabled")
		return
	}
	if !secureEqual(r.Header.Get("X-SwarmJet-Node-Secret"), a.cfg.NodeSecret) {
		problem(w, 401, "node authentication required")
		return
	}
	var n domain.Node
	if !decode(w, r, &n) {
		return
	}
	if !domain.IDPattern.MatchString(n.ID) || n.Address == "" || n.Capacity < 1 || n.Capacity > 128 {
		problem(w, 400, "valid node id, address and capacity are required")
		return
	}
	if len(n.Address) > 512 || strings.IndexFunc(n.Address, unicode.IsSpace) >= 0 {
		problem(w, 400, "invalid node address")
		return
	}
	if !strings.HasPrefix(n.Address, "http://") && !strings.HasPrefix(n.Address, "https://") {
		problem(w, 400, "node address must be an http(s) URL")
		return
	}
	n.LastSeen = time.Now().UTC()
	if err := a.s.HeartbeatNode(r.Context(), n); err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, map[string]bool{"registered": true})
}

func (a *server) nodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return
	}
	if a.s == nil {
		problem(w, 503, "execution node registry is unavailable")
		return
	}
	statuses, err := a.s.NodeStatuses(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, statuses)
}
