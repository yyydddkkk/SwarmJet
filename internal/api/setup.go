package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/onboarding"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// setupCells and setupCLILogin mirror the contract's status blocks. The CLI
// login task fills the second block through api.Config.CLILoginStatus.
type setupCells struct {
	Enabled         bool   `json:"enabled"`
	Image           string `json:"image"`
	Model           string `json:"model"`
	DockerAvailable bool   `json:"dockerAvailable"`
}

type setupCLILogin struct {
	Available bool   `json:"available"`
	LoggedIn  bool   `json:"loggedIn"`
	CLI       string `json:"cli"`
}

type setupStatusResponse struct {
	GitHub   onboarding.GitHubStatus `json:"github"`
	Tunnel   onboarding.TunnelStatus `json:"tunnel"`
	Cells    setupCells              `json:"cells"`
	CLILogin setupCLILogin           `json:"cliLogin"`
}

// setup routes the admin-only setup endpoints. The GitHub callback is handled
// earlier because GitHub navigates the browser to it cross-site.
func (a *server) setup(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/setup/status":
		a.setupStatus(w, r)
	case "/api/setup/github/manifest":
		a.setupGitHubManifest(w, r)
	case "/api/setup/github/repositories":
		a.setupGitHubRepositories(w, r)
	default:
		problem(w, http.StatusNotFound, "API route not found")
	}
}

func (a *server) setupStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	response := setupStatusResponse{
		Cells: setupCells{
			Enabled:         a.cfg.CellsEnabled,
			Image:           a.cfg.CellsImage,
			Model:           a.cfg.CellsModel,
			DockerAvailable: a.cfg.DockerAvailable != nil && a.cfg.DockerAvailable(),
		},
		CLILogin: setupCLILogin{CLI: "opencode"},
	}
	if a.cfg.CLILoginStatus != nil {
		response.CLILogin.Available, response.CLILogin.LoggedIn = a.cfg.CLILoginStatus()
	}
	if a.cfg.Setup != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		response.GitHub = a.cfg.Setup.GitHubStatus(ctx)
		response.Tunnel = a.cfg.Setup.TunnelStatus()
	}
	respond(w, http.StatusOK, response)
}

func (a *server) setupGitHubManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.cfg.Setup == nil {
		problem(w, http.StatusServiceUnavailable, "GitHub App setup is unavailable")
		return
	}
	var req struct {
		Owner string `json:"owner"`
	}
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	response, err := a.cfg.Setup.CreateManifest(ctx, req.Owner)
	if err != nil {
		switch {
		case errors.Is(err, onboarding.ErrInvalidOwner):
			problem(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, onboarding.ErrInvalidOrigin):
			problem(w, http.StatusInternalServerError, "PUBLIC_ORIGIN 配置无效")
		case errors.Is(err, onboarding.ErrMasterKey):
			problem(w, http.StatusServiceUnavailable, err.Error())
		default:
			problem(w, http.StatusInternalServerError, "无法生成 GitHub App 创建请求")
		}
		return
	}
	// A SameSite=Lax cookie is required for the GitHub redirect back to this
	// origin; the normal session cookie is Strict and is not sent there.
	http.SetCookie(w, &http.Cookie{
		Name:     onboarding.CallbackCookieName,
		Value:    response.CallbackToken,
		Path:     onboarding.CallbackPath,
		HttpOnly: true,
		Secure:   a.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(onboarding.ManifestStateTTL / time.Second),
	})
	respond(w, http.StatusOK, response)
}

// setupGitHubCallback is called from GitHub's cross-site redirect. A valid
// administrator session may complete the flow with the one-time state alone.
// Without the session cookie (GitHub's redirect does not carry the
// SameSite=Strict session cookie), the state must also match the short-lived
// SameSite=Lax callback cookie issued when the manifest was created.
func (a *server) setupGitHubCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.cfg.Setup == nil {
		redirectSetup(w, r, "github=error", "setup_unavailable")
		return
	}
	adminSession := false
	if _, err := r.Cookie("swarmjet_session"); err == nil {
		principal, err := a.sessionPrincipal(r)
		if err != nil || (!principal.PlatformAdmin && !principal.Breakglass) {
			redirectSetup(w, r, "github=error", "forbidden")
			return
		}
		adminSession = true
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	var err error
	if adminSession {
		err = a.cfg.Setup.HandleCallbackAdmin(r.Context(), code, state)
	} else {
		cookie, cookieErr := r.Cookie(onboarding.CallbackCookieName)
		if cookieErr != nil || cookie.Value == "" {
			redirectSetup(w, r, "github=error", "invalid_state")
			return
		}
		err = a.cfg.Setup.HandleCallback(r.Context(), code, state, cookie.Value)
	}
	if err != nil {
		errorCode := "callback_failed"
		switch {
		case errors.Is(err, store.ErrManifestStateInvalid):
			errorCode = "invalid_state"
		case errors.Is(err, onboarding.ErrMasterKey):
			errorCode = "master_key_missing"
		}
		// Never log the state, code or credential material.
		slog.Warn("GitHub App manifest callback failed", "reason", errorCode, "error", err)
		redirectSetup(w, r, "github=error", errorCode)
		return
	}
	// Clear the one-time binding cookie.
	http.SetCookie(w, &http.Cookie{Name: onboarding.CallbackCookieName, Value: "", Path: onboarding.CallbackPath, MaxAge: -1, HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})
	redirectSetup(w, r, "github=connected", "")
}

func redirectSetup(w http.ResponseWriter, r *http.Request, status, code string) {
	target := "/#/setup?" + status
	if code != "" {
		target += "&error=" + url.QueryEscape(code)
	}
	http.Redirect(w, r, target, http.StatusFound)
}

type setupRepository struct {
	FullName      string `json:"fullName"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"defaultBranch"`
}

func (a *server) setupGitHubRepositories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.cfg.Setup == nil {
		problem(w, http.StatusServiceUnavailable, "GitHub App setup is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	repositories, err := a.cfg.Setup.Repositories(ctx)
	if err != nil {
		if errors.Is(err, onboarding.ErrNotConfigured) {
			problem(w, http.StatusConflict, err.Error())
			return
		}
		problem(w, http.StatusBadGateway, "无法读取 GitHub 仓库列表")
		return
	}
	out := make([]setupRepository, 0, len(repositories))
	for _, repository := range repositories {
		out = append(out, setupRepository{FullName: repository.FullName, Private: repository.Private, DefaultBranch: repository.DefaultBranch})
	}
	respond(w, http.StatusOK, map[string]any{"repositories": out})
}
