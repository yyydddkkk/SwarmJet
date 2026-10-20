package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology"
)

// Methodology console endpoints (round4-prompts contract section 7):
//
//	GET  /api/hives/{id}/methodology?version=
//	GET  /api/hives/{id}/methodology/versions
//	POST /api/hives/{id}/methodology/activate
//
// The console has one administrator account, so the existing session check in
// serve() is the only authorization here.

const maxMethodologyNoteRunes = 2000

type methodologyResponse struct {
	domain.Methodology
	Files   []domain.MethodologyFile `json:"files"`
	Builtin bool                     `json:"builtin,omitempty"`
}

func (a *server) methodology(w http.ResponseWriter, r *http.Request, hiveID string, rest []string) {
	if a.s == nil {
		problem(w, http.StatusServiceUnavailable, "hive storage unavailable")
		return
	}
	switch {
	case len(rest) == 0 && r.Method == http.MethodGet:
		a.getMethodology(w, r, hiveID)
	case len(rest) == 1 && rest[0] == "versions" && r.Method == http.MethodGet:
		a.listMethodologyVersions(w, r, hiveID)
	case len(rest) == 1 && rest[0] == "activate" && r.Method == http.MethodPost:
		a.activateMethodology(w, r, hiveID)
	case len(rest) == 0 || (len(rest) == 1 && (rest[0] == "versions" || rest[0] == "activate")):
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
	default:
		problem(w, http.StatusNotFound, "API route not found")
	}
}

func (a *server) getMethodology(w http.ResponseWriter, r *http.Request, hiveID string) {
	if _, err := a.s.Hive(r.Context(), hiveID); err != nil {
		hiveFailure(w, err)
		return
	}
	version := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("version")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			problem(w, http.StatusBadRequest, "version must be a positive integer")
			return
		}
		version = parsed
	}

	var m domain.Methodology
	var files []domain.MethodologyFile
	builtin := false
	var err error
	if version > 0 {
		m, files, err = a.s.MethodologyVersion(r.Context(), hiveID, version)
	} else {
		m, files, err = a.s.ActiveMethodology(r.Context(), hiveID)
		if errors.Is(err, domain.ErrNotFound) {
			manifest, builtinFiles := methodology.Builtin()
			m = domain.Methodology{HiveID: hiveID, Manifest: manifest}
			files, builtin, err = builtinFiles, true, nil
		}
	}
	if err != nil {
		hiveFailure(w, err)
		return
	}
	if files == nil {
		files = []domain.MethodologyFile{}
	}
	respond(w, http.StatusOK, methodologyResponse{Methodology: m, Files: files, Builtin: builtin})
}

func (a *server) listMethodologyVersions(w http.ResponseWriter, r *http.Request, hiveID string) {
	if _, err := a.s.Hive(r.Context(), hiveID); err != nil {
		hiveFailure(w, err)
		return
	}
	versions, err := a.s.MethodologyVersions(r.Context(), hiveID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	changes, err := a.s.MethodologyChanges(r.Context(), hiveID)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	if versions == nil {
		versions = []domain.Methodology{}
	}
	if changes == nil {
		changes = []methodology.Change{}
	}
	activeVersion := 0
	for _, version := range versions {
		if version.Active {
			activeVersion = version.Version
			break
		}
	}
	respond(w, http.StatusOK, map[string]any{
		"hiveId":        hiveID,
		"activeVersion": activeVersion,
		"versions":      versions,
		"changes":       changes,
	})
}

func (a *server) activateMethodology(w http.ResponseWriter, r *http.Request, hiveID string) {
	var req struct {
		Version int    `json:"version"`
		Note    string `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Version <= 0 {
		problem(w, http.StatusBadRequest, "version is required")
		return
	}
	if utf8.RuneCountInString(req.Note) > maxMethodologyNoteRunes {
		problem(w, http.StatusUnprocessableEntity, fmt.Sprintf("note is longer than %d characters", maxMethodologyNoteRunes))
		return
	}
	actor := a.webLocalAddress(r)
	active, err := a.s.ActivateMethodology(r.Context(), hiveID, req.Version, actor, req.Note)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	if err = a.notifyMethodologyChange(r.Context(), hiveID, methodology.ActionActivate, active.Version, req.Note); err != nil {
		problem(w, http.StatusInternalServerError,
			fmt.Sprintf("activated version %d but could not notify the Hive: %v", active.Version, err))
		return
	}
	_, files, err := a.s.MethodologyVersion(r.Context(), hiveID, active.Version)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	if files == nil {
		files = []domain.MethodologyFile{}
	}
	respond(w, http.StatusOK, methodologyResponse{Methodology: active, Files: files})
}

func (a *server) notifyMethodologyChange(ctx context.Context, hiveID, action string, version int, note string) error {
	body := fmt.Sprintf("methodology %s: version %d via the console", action, version)
	if note = strings.TrimSpace(note); note != "" {
		body += ": " + note
	}
	_, err := a.s.SendMessage(ctx, domain.Message{
		HiveID: hiveID,
		From:   domain.AddressSystem,
		To:     domain.AddressHive,
		Kind:   domain.MessageEvent,
		Body:   truncateRunes(body, domain.MaxMessageBody),
		Trust:  domain.TrustSystem,
	})
	return err
}
