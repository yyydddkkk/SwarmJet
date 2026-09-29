package api

import (
	"net/http"
	"strings"

	"swarmjet/internal/domain"
	"swarmjet/internal/store"
)

func (a *server) audit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		problem(w, 405, "method not allowed")
		return
	}
	projectID := r.URL.Query().Get("projectId")
	if projectID != "" && !domain.IDPattern.MatchString(projectID) {
		problem(w, 400, "invalid project ID")
		return
	}
	events, err := a.s.AuditEvents(r.Context(), projectID, 100)
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, 200, map[string]any{"events": events})
}

func (a *server) teams(w http.ResponseWriter, r *http.Request, tail []string) {
	if len(tail) == 0 {
		switch r.Method {
		case http.MethodGet:
			teams, err := a.s.Teams(r.Context())
			if err != nil {
				failure(w, err)
				return
			}
			respond(w, 200, map[string]any{"teams": teams})
		case http.MethodPost:
			var req struct {
				Name string `json:"name"`
			}
			if !decode(w, r, &req) {
				return
			}
			id, err := newID()
			if err == nil {
				err = a.s.CreateTeam(r.Context(), id, req.Name)
			}
			if err != nil {
				failure(w, err)
				return
			}
			_ = a.s.Audit(r.Context(), requestPrincipal(r), "team.create", "team", id, map[string]string{"name": req.Name})
			respond(w, 201, map[string]string{"id": id, "name": strings.TrimSpace(req.Name)})
		default:
			problem(w, 405, "method not allowed")
		}
		return
	}
	if !domain.IDPattern.MatchString(tail[0]) || len(tail) < 2 {
		problem(w, 404, "not found")
		return
	}
	teamID := tail[0]
	if len(tail) == 2 && tail[1] == "members" && r.Method == http.MethodPut {
		var req struct {
			UserID string `json:"userId"`
		}
		if !decode(w, r, &req) {
			return
		}
		if !validAccountID(req.UserID) {
			problem(w, 400, "invalid user ID")
			return
		}
		if err := a.s.AddTeamMember(r.Context(), teamID, req.UserID); err != nil {
			failure(w, err)
			return
		}
		_ = a.s.Audit(r.Context(), requestPrincipal(r), "team.member.add", "team", teamID, map[string]string{"userId": req.UserID})
		w.WriteHeader(204)
		return
	}
	if len(tail) == 3 && tail[1] == "members" && r.Method == http.MethodDelete {
		if !validAccountID(tail[2]) {
			problem(w, 404, "not found")
			return
		}
		if err := a.s.DeleteTeamMember(r.Context(), teamID, tail[2]); err != nil {
			failure(w, err)
			return
		}
		_ = a.s.Audit(r.Context(), requestPrincipal(r), "team.member.remove", "team", teamID, map[string]string{"userId": tail[2]})
		w.WriteHeader(204)
		return
	}
	if len(tail) == 3 && tail[1] == "projects" && domain.IDPattern.MatchString(tail[2]) {
		projectID := tail[2]
		switch r.Method {
		case http.MethodPut:
			var req struct {
				Role store.ProjectRole `json:"role"`
			}
			if !decode(w, r, &req) {
				return
			}
			if err := a.s.SetTeamProjectGrant(r.Context(), teamID, projectID, req.Role); err != nil {
				failure(w, err)
				return
			}
			_ = a.s.Audit(r.Context(), requestPrincipal(r), "team.project.grant", "project", projectID, map[string]any{"teamId": teamID, "role": req.Role})
			w.WriteHeader(204)
		case http.MethodDelete:
			if err := a.s.DeleteTeamProjectGrant(r.Context(), teamID, projectID); err != nil {
				failure(w, err)
				return
			}
			_ = a.s.Audit(r.Context(), requestPrincipal(r), "team.project.revoke", "project", projectID, map[string]string{"teamId": teamID})
			w.WriteHeader(204)
		default:
			problem(w, 405, "method not allowed")
		}
		return
	}
	problem(w, 404, "not found")
}

func (a *server) projectMembers(w http.ResponseWriter, r *http.Request, projectID string, tail []string) {
	if len(tail) == 0 && r.Method == http.MethodGet {
		members, err := a.s.ProjectMembers(r.Context(), projectID)
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, 200, map[string]any{"members": members})
		return
	}
	if len(tail) != 1 || !validAccountID(tail[0]) {
		problem(w, 404, "not found")
		return
	}
	userID := tail[0]
	switch r.Method {
	case http.MethodPut:
		var req struct {
			Role store.ProjectRole `json:"role"`
		}
		if !decode(w, r, &req) {
			return
		}
		if err := a.s.SetProjectGrant(r.Context(), projectID, userID, req.Role); err != nil {
			failure(w, err)
			return
		}
		_ = a.s.Audit(r.Context(), requestPrincipal(r), "project.member.grant", "project", projectID, map[string]any{"userId": userID, "role": req.Role})
		w.WriteHeader(204)
	case http.MethodDelete:
		if err := a.s.DeleteProjectGrant(r.Context(), projectID, userID); err != nil {
			failure(w, err)
			return
		}
		_ = a.s.Audit(r.Context(), requestPrincipal(r), "project.member.revoke", "project", projectID, map[string]string{"userId": userID})
		w.WriteHeader(204)
	default:
		problem(w, 405, "method not allowed")
	}
}

func (a *server) triggerPolicy(w http.ResponseWriter, r *http.Request, id string) {
	p, err := a.s.Project(r.Context(), id)
	if err != nil {
		failure(w, err)
		return
	}
	if r.Method == http.MethodGet {
		mode := p.GitLabTriggerMode
		if mode == "" {
			mode = "off"
		}
		respond(w, 200, map[string]any{"gitlabTriggerMode": mode, "githubAutoIntake": p.GitHubAutoIntake})
		return
	}
	if r.Method != http.MethodPut {
		problem(w, 405, "method not allowed")
		return
	}
	var req struct {
		GitLabTriggerMode string `json:"gitlabTriggerMode"`
		GitHubAutoIntake  bool   `json:"githubAutoIntake"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !validGitLabTriggerMode(req.GitLabTriggerMode) {
		problem(w, 400, "invalid GitLab trigger mode")
		return
	}
	if req.GitHubAutoIntake != p.GitHubAutoIntake && !requestPrincipal(r).PlatformAdmin {
		problem(w, 403, "platform administrator required to change GitHub intake")
		return
	}
	p.GitLabTriggerMode = req.GitLabTriggerMode
	p.GitHubAutoIntake = req.GitHubAutoIntake
	saved, err := a.s.SaveProject(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	_ = a.s.Audit(r.Context(), requestPrincipal(r), "project.trigger-policy.update", "project", id, map[string]any{"gitlabTriggerMode": saved.GitLabTriggerMode, "githubAutoIntake": saved.GitHubAutoIntake})
	respond(w, 200, map[string]any{"gitlabTriggerMode": saved.GitLabTriggerMode, "githubAutoIntake": saved.GitHubAutoIntake})
}

func validGitLabTriggerMode(mode string) bool {
	switch mode {
	case "off", "owner", "operator", "all":
		return true
	default:
		return false
	}
}
