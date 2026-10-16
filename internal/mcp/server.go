package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// tokenContextKey carries the authenticated token from the auth middleware to
// the per-session server factory.
type tokenContextKey struct{}

// NewHandler returns an http.Handler that serves the SwarmJet MCP endpoint over
// the Streamable HTTP transport. It is not mounted on the API server this round;
// wiring is deferred to the next round.
//
// Every request must carry "Authorization: Bearer <token>". The token is
// resolved through Backend.TokenByPlaintext and its scope gates the tools:
// create_hive and update_hive need an admin token (ADR section 12).
func NewHandler(backend Backend, opts Options) http.Handler {
	if backend == nil {
		panic("mcp: nil backend")
	}
	o := opts.withDefaults()
	streamable := mcpsdk.NewStreamableHTTPHandler(func(r *http.Request) *mcpsdk.Server {
		tok, ok := tokenFromContext(r.Context())
		if !ok {
			// The auth middleware guarantees a token; returning nil makes the
			// SDK answer 400 rather than panic.
			return nil
		}
		return newServer(backend, o, tok)
	}, &mcpsdk.StreamableHTTPOptions{
		Logger:         o.Logger,
		SessionTimeout: o.SessionTimeout,
	})
	return &authHandler{backend: backend, logger: o.Logger, next: streamable}
}

func newServer(backend Backend, opts Options, tok domain.Token) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name:        opts.Name,
		Version:     opts.Version,
		Description: "SwarmJet Hive control plane: talk to a Queen, spawn and inspect Foragers.",
	}, &mcpsdk.ServerOptions{Logger: opts.Logger})
	ts := &toolset{backend: backend, opts: opts, token: tok}
	ts.register(s)
	return s
}

func tokenFromContext(ctx context.Context) (domain.Token, bool) {
	tok, ok := ctx.Value(tokenContextKey{}).(domain.Token)
	return tok, ok
}

// authHandler authenticates the bearer token before the Streamable HTTP handler
// creates a session, so a session is always bound to the token that opened it.
type authHandler struct {
	backend Backend
	logger  *slog.Logger
	next    http.Handler
}

func (h *authHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	plaintext, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		h.deny(w, "missing or malformed bearer token")
		return
	}
	tok, err := h.backend.TokenByPlaintext(r.Context(), plaintext)
	if err != nil {
		h.deny(w, "invalid token")
		return
	}
	ctx := context.WithValue(r.Context(), tokenContextKey{}, tok)
	h.next.ServeHTTP(w, r.WithContext(ctx))
}

func (h *authHandler) deny(w http.ResponseWriter, reason string) {
	if h.logger != nil {
		h.logger.Debug("mcp: rejected request", "reason", reason)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="swarmjet"`)
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = fmt.Fprintf(w, `{"error":"unauthorized","reason":%q}`, reason)
}

// bearerToken extracts the token from an Authorization header.
func bearerToken(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}
