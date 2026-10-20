package api

import (
	"bytes"
	"encoding/json"
	"net/http"
)

type updateHiveRequest struct {
	QueenEnabled *bool           `json:"queenEnabled"`
	Config       json.RawMessage `json:"config"`
}

// updateHive is covered by the console's administrator session and CSRF gates.
// Decode the config into the current value so omitted fields survive updates.
func (a *server) updateHive(w http.ResponseWriter, r *http.Request, hiveID string) {
	var req updateHiveRequest
	if !decode(w, r, &req) {
		return
	}
	hive, err := a.s.Hive(r.Context(), hiveID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	if req.QueenEnabled != nil {
		hive.QueenEnabled = *req.QueenEnabled
	}
	if len(req.Config) > 0 {
		if bytes.Equal(bytes.TrimSpace(req.Config), []byte("null")) {
			problem(w, http.StatusBadRequest, "config must be an object")
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(req.Config))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&hive.Config); err != nil {
			problem(w, http.StatusBadRequest, "invalid config: "+err.Error())
			return
		}
	}
	if msg := validateHiveConfig(hive.Config); msg != "" {
		problem(w, http.StatusUnprocessableEntity, msg)
		return
	}
	updated, err := a.s.UpdateHive(r.Context(), hive)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"hive": updated, "localAddress": string(a.webLocalAddress(r))})
}
