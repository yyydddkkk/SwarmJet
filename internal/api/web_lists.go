package api

import (
	"net/http"
	"strconv"
	"time"
)

func (a *server) hiveWebList(w http.ResponseWriter, r *http.Request, hiveID, kind string) {
	if r.Method != http.MethodGet {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, err := a.s.Hive(r.Context(), hiveID); err != nil {
		hiveFailure(w, err)
		return
	}
	switch kind {
	case "queue":
		entries, err := a.s.SpawnQueue(r.Context(), hiveID)
		if err != nil {
			hiveFailure(w, err)
			return
		}
		respond(w, http.StatusOK, map[string]any{"queue": entries})
	case "events":
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 1000 {
				problem(w, http.StatusBadRequest, "limit must be between 1 and 1000")
				return
			}
			limit = parsed
		}
		events, err := a.s.MethodologyEvents(r.Context(), hiveID, limit)
		if err != nil {
			hiveFailure(w, err)
			return
		}
		respond(w, http.StatusOK, map[string]any{"events": events})
	case "signals":
		since := time.Now().UTC().Add(-24 * time.Hour)
		if raw := r.URL.Query().Get("since"); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				problem(w, http.StatusBadRequest, "since must be RFC3339")
				return
			}
			since = parsed
		}
		signals, err := a.s.Signals(r.Context(), hiveID, since)
		if err != nil {
			hiveFailure(w, err)
			return
		}
		respond(w, http.StatusOK, map[string]any{"signals": signals})
	}
}
