package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/store"
)

type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *auditResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *auditResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (a *server) auditMutation(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, func(), bool) {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return w, func() {}, true
	}
	principal := requestPrincipal(r)
	path := r.URL.Path
	action := "http." + r.Method
	targetType, targetID := a.mutationTarget(r)
	if err := a.s.Audit(r.Context(), principal, action+".attempt", targetType, targetID, map[string]string{"route": path}); err != nil {
		failure(w, err)
		return w, nil, false
	}
	writer := &auditResponseWriter{ResponseWriter: w}
	finish := func() {
		status := writer.status
		if status == 0 {
			status = http.StatusOK
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
		defer cancel()
		if err := a.s.Audit(ctx, principal, action+".result", targetType, targetID, map[string]any{"route": path, "status": status}); err != nil {
			slog.Error("authorization audit result failed", "path", path, "status", status, "error", err)
		}
	}
	return writer, finish, true
}

func (a *server) mutationTarget(r *http.Request) (string, string) {
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	parts := strings.Split(path, "/")
	if len(parts) >= 3 && parts[0] == "workbench" && parts[2] != "" {
		return "project", parts[2]
	}
	if len(parts) >= 2 && parts[0] == "projects" && parts[1] != "discover" {
		return "project", parts[1]
	}
	if len(parts) >= 2 && parts[0] == "tasks" {
		if task, err := a.s.Task(r.Context(), parts[1]); err == nil {
			return "project", task.Project.ID
		}
	}
	return "route", r.URL.Path
}

type principalContextKey struct{}

func withPrincipal(r *http.Request, principal store.Principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal))
}

func requestPrincipal(r *http.Request) store.Principal {
	principal, _ := r.Context().Value(principalContextKey{}).(store.Principal)
	return principal
}

func (a *server) sessionPrincipal(r *http.Request) (store.Principal, error) {
	if a.s == nil {
		return store.Principal{}, store.ErrNotFound
	}
	cookie, err := r.Cookie("swarmjet_session")
	if err != nil || len(cookie.Value) != 64 {
		return store.Principal{}, store.ErrNotFound
	}
	return a.s.SessionPrincipal(r.Context(), tokenHash(cookie.Value))
}

func (a *server) projectRole(r *http.Request, projectID string) (store.ProjectRole, error) {
	principal := requestPrincipal(r)
	if principal.PlatformAdmin || principal.Breakglass {
		return store.RoleOwner, nil
	}
	if principal.UserID == "" || projectID == "" {
		return "", nil
	}
	return a.s.ProjectRole(r.Context(), principal.UserID, projectID)
}

func (a *server) requireProjectRole(w http.ResponseWriter, r *http.Request, projectID string, minimum store.ProjectRole) bool {
	role, err := a.projectRole(r, projectID)
	if err != nil {
		failure(w, err)
		return false
	}
	if roleRankAPI(role) < roleRankAPI(minimum) {
		// Project existence is not disclosed to callers who cannot view it.
		problem(w, http.StatusNotFound, "not found")
		return false
	}
	return true
}

func roleRankAPI(role store.ProjectRole) int {
	switch role {
	case store.RoleViewer:
		return 1
	case store.RoleOperator:
		return 2
	case store.RoleOwner:
		return 3
	default:
		return 0
	}
}

func (a *server) requirePlatformAdmin(w http.ResponseWriter, r *http.Request) bool {
	principal := requestPrincipal(r)
	if principal.PlatformAdmin || principal.Breakglass {
		return true
	}
	problem(w, http.StatusForbidden, "platform administrator required")
	return false
}

func (a *server) visibleProjects(r *http.Request) (map[string]store.ProjectRole, error) {
	principal := requestPrincipal(r)
	if principal.PlatformAdmin || principal.Breakglass {
		return nil, nil
	}
	if principal.UserID == "" {
		return map[string]store.ProjectRole{}, nil
	}
	return a.s.VisibleProjectIDs(r.Context(), principal.UserID)
}

func (a *server) ownsAnyProject(r *http.Request) (bool, error) {
	projects, err := a.visibleProjects(r)
	if err != nil {
		return false, err
	}
	if projects == nil {
		return true, nil
	}
	for _, role := range projects {
		if role == store.RoleOwner {
			return true, nil
		}
	}
	return false, nil
}

// authorizeRoute is the deny-by-default gate for browser API routes. Resource
// handlers still check operations whose project or command is in the body.
func (a *server) authorizeRoute(w http.ResponseWriter, r *http.Request) bool {
	path := strings.TrimPrefix(r.URL.Path, "/api/")
	if path == "session" || path == "logout" {
		return true
	}
	if path == "users" && r.Method == http.MethodGet {
		if status := r.URL.Query().Get("status"); status != "" && status != "active" {
			return a.requirePlatformAdmin(w, r)
		}
		owner, err := a.ownsAnyProject(r)
		if err != nil {
			failure(w, err)
			return false
		}
		if owner {
			return true
		}
		problem(w, http.StatusForbidden, "project owner required")
		return false
	}
	if path == "users" || strings.HasPrefix(path, "users/") || path == "teams" || strings.HasPrefix(path, "teams/") {
		return a.requirePlatformAdmin(w, r)
	}
	if path == "projects" {
		if r.Method == http.MethodGet {
			return true
		}
		return a.requirePlatformAdmin(w, r)
	}
	if path == "tasks" {
		return true // Query and create paths resolve their project in the handler.
	}
	// Hive/Forager management is platform-admin only this round. Per-Hive roles
	// will replace this gate when Hive membership exists.
	if path == "hives" || strings.HasPrefix(path, "hives/") || strings.HasPrefix(path, "foragers/") {
		return a.requirePlatformAdmin(w, r)
	}
	if path == "gitlab-identities" {
		return true // Identity handlers use the authenticated principal.
	}
	if strings.HasPrefix(path, "projects/") {
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			problem(w, 404, "not found")
			return false
		}
		minimum := store.RoleViewer
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			minimum = store.RoleOwner
		}
		if len(parts) >= 3 && parts[2] == "members" {
			minimum = store.RoleOwner
		}
		return a.requireProjectRole(w, r, parts[1], minimum)
	}
	if strings.HasPrefix(path, "tasks/") {
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			problem(w, 404, "not found")
			return false
		}
		task, err := a.s.Task(r.Context(), parts[1])
		if errors.Is(err, store.ErrNotFound) {
			problem(w, 404, "not found")
			return false
		}
		if err != nil {
			failure(w, err)
			return false
		}
		minimum := store.RoleViewer
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			minimum = store.RoleOperator
		}
		if len(parts) >= 3 && parts[2] == "manual-acceptance" && r.Method != http.MethodGet {
			minimum = store.RoleOwner
		}
		return a.requireProjectRole(w, r, task.Project.ID, minimum)
	}
	if strings.HasPrefix(path, "workbench/") {
		parts := strings.Split(path, "/")
		if len(parts) <= 2 {
			return true // Collection operations filter or resolve a project later.
		}
		minimum := store.RoleViewer
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			minimum = store.RoleOperator
		}
		if len(parts) >= 6 && (parts[5] == "approve" || parts[5] == "unapprove" || parts[5] == "merge") {
			minimum = store.RoleOwner
		}
		return a.requireProjectRole(w, r, parts[2], minimum)
	}
	if path == "audit" {
		projectID := r.URL.Query().Get("projectId")
		if projectID == "" {
			return a.requirePlatformAdmin(w, r)
		}
		return a.requireProjectRole(w, r, projectID, store.RoleOwner)
	}
	return a.requirePlatformAdmin(w, r)
}
