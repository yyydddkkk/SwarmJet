package api

import (
	"net/http"
	"swarmjet/internal/agentcli"
	"swarmjet/internal/domain"
)

func (a *server) agents(w http.ResponseWriter, r *http.Request) {
	if r.Method == "PUT" {
		var req struct {
			Enabled []string `json:"enabled"`
		}
		if !decode(w, r, &req) {
			return
		}
		if err := domain.ValidateEnabledAgents(req.Enabled); err != nil {
			problem(w, 400, err.Error())
			return
		}
		if err := a.s.SetEnabledAgents(r.Context(), req.Enabled); err != nil {
			failure(w, err)
			return
		}
	} else if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return
	}
	ids, err := a.s.EnabledAgents(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, map[string]any{"agents": agentcli.Catalog(), "enabled": ids})
}
