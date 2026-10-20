package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/llmproxy"
)

type queenStatus struct {
	Enabled    bool              `json:"enabled"`
	State      domain.AgentState `json:"state"`
	Model      string            `json:"model"`
	AgentID    string            `json:"agentId"`
	LastTurnAt *time.Time        `json:"lastTurnAt"`
}

func (a *server) hiveQueen(w http.ResponseWriter, r *http.Request, hiveID string) {
	principal := requestPrincipal(r)
	if !principal.PlatformAdmin && !principal.Breakglass {
		problem(w, http.StatusForbidden, "administrator session required")
		return
	}
	if r.Method != http.MethodGet {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	hive, err := a.s.Hive(r.Context(), hiveID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	queen, err := a.s.Queen(r.Context(), hiveID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	model := strings.TrimSpace(hive.Config.QueenModel)
	if model == "" {
		model = strings.TrimSpace(a.cfg.DefaultModel)
	}
	if model == "" {
		model = llmproxy.DefaultModel
	}
	status := queenStatus{Enabled: hive.QueenEnabled, State: queen.State, Model: model, AgentID: queen.ID}
	// Each successfully finished turn emits a report. State updates and other
	// Queen messages do not imply a completed turn.
	if err = a.s.Pool.QueryRow(r.Context(), `SELECT max(created_at) FROM messages WHERE hive_id=$1 AND from_addr='queen' AND kind='report'`, hiveID).Scan(&status.LastTurnAt); err != nil {
		hiveFailure(w, err)
		return
	}
	respond(w, http.StatusOK, status)
}
