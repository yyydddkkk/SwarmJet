package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology"
	"github.com/yyydddkkk/SwarmJet/internal/methodology/catalog"
)

func (a *server) methodologyTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	templates, err := catalog.List()
	if err != nil {
		problem(w, http.StatusInternalServerError, err.Error())
		return
	}
	if templates == nil {
		templates = []domain.MethodologyTemplate{}
	}
	respond(w, http.StatusOK, templates)
}

func (a *server) installMethodologyTemplate(w http.ResponseWriter, r *http.Request, hiveID string) {
	var req struct {
		Name string `json:"name"`
		Note string `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		problem(w, http.StatusBadRequest, "name is required")
		return
	}
	if utf8.RuneCountInString(req.Note) > maxMethodologyNoteRunes {
		problem(w, http.StatusUnprocessableEntity, fmt.Sprintf("note is longer than %d characters", maxMethodologyNoteRunes))
		return
	}
	if _, err := a.s.Hive(r.Context(), hiveID); err != nil {
		hiveFailure(w, err)
		return
	}
	template, manifest, files, err := catalog.Get(req.Name)
	if err != nil {
		if errors.Is(err, catalog.ErrNotFound) {
			problem(w, http.StatusNotFound, "methodology template not found")
		} else {
			problem(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	published, err := a.s.PublishMethodology(r.Context(), domain.Methodology{
		HiveID:    hiveID,
		Source:    &domain.MethodologySource{URL: "builtin:" + template.Name, Ref: template.Version},
		Manifest:  manifest,
		Note:      req.Note,
		CreatedBy: a.webLocalAddress(r),
	}, files)
	if err != nil {
		hiveFailure(w, err)
		return
	}
	if err = a.notifyMethodologyChange(r.Context(), hiveID, methodology.ActionPublish, published.Version, req.Note); err != nil {
		problem(w, http.StatusInternalServerError,
			fmt.Sprintf("installed version %d but could not notify the Hive: %v", published.Version, err))
		return
	}
	if files == nil {
		files = []domain.MethodologyFile{}
	}
	respond(w, http.StatusCreated, methodologyResponse{Methodology: published, Files: files})
}
