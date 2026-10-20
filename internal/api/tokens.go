package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/connect"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

const maxTokenName = 200

// tokenIDPattern matches the opaque IDs minted by the store. It mirrors the
// identifier policy the rest of the console uses.
var tokenIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

type tokenCreateRequest struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

type tokenCreateResponse struct {
	Token     domain.Token   `json:"token"`
	Plaintext string         `json:"plaintext"`
	Connect   connect.Bundle `json:"connect"`
}

// tokens implements GET and POST /api/tokens. The plaintext exists only in the
// POST response; it is never stored or included in the list.
func (a *server) tokens(w http.ResponseWriter, r *http.Request) {
	if a.s == nil {
		problem(w, http.StatusServiceUnavailable, "token storage unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		tokens, err := a.s.ListTokens(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, http.StatusOK, map[string]any{"tokens": tokens})
	case http.MethodPost:
		var req tokenCreateRequest
		if !decode(w, r, &req) {
			return
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			problem(w, http.StatusBadRequest, "--name is required")
			return
		}
		if len(name) > maxTokenName {
			problem(w, http.StatusBadRequest, "name must be at most 200 characters")
			return
		}
		scope := domain.TokenScope(strings.ToLower(strings.TrimSpace(req.Scope)))
		if scope == "" {
			scope = domain.TokenDefault
		}
		if scope != domain.TokenDefault && scope != domain.TokenAdmin {
			problem(w, http.StatusBadRequest, "invalid scope: want default or admin")
			return
		}
		plaintext, token, err := a.s.CreateToken(r.Context(), name, scope)
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, http.StatusCreated, tokenCreateResponse{
			Token:     token,
			Plaintext: plaintext,
			Connect:   a.connectionBundle(plaintext),
		})
	default:
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// tokenItem implements DELETE /api/tokens/{id}.
func (a *server) tokenItem(w http.ResponseWriter, r *http.Request, id string) {
	if a.s == nil {
		problem(w, http.StatusServiceUnavailable, "token storage unavailable")
		return
	}
	if r.Method != http.MethodDelete {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !tokenIDPattern.MatchString(id) {
		problem(w, http.StatusBadRequest, "invalid token id")
		return
	}
	if err := a.s.RevokeToken(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "not found")
			return
		}
		failure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// connectionBundle renders the one-time local Agent configurations. The caller
// must have just minted plaintext and must return it only in this response.
func (a *server) connectionBundle(plaintext string) connect.Bundle {
	return connect.NewBundle(a.mcpEndpoint(), plaintext)
}

// mcpEndpoint resolves the endpoint embedded in client configs. The console
// talks to the same origin; PUBLIC_ORIGIN provides the externally reachable
// address when the API is behind a tunnel or reverse proxy.
func (a *server) mcpEndpoint() string {
	if value := strings.TrimSpace(a.cfg.MCPURL); value != "" {
		return value
	}
	origin := strings.TrimRight(strings.TrimSpace(a.cfg.AllowedOrigin), "/")
	if origin == "" {
		origin = "http://127.0.0.1:8180"
	}
	return origin + "/mcp"
}
