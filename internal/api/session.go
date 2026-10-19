package api

import (
	"context"
	"net/http"

	"github.com/yyydddkkk/SwarmJet/internal/store"
)

type principalContextKey struct{}

func withPrincipal(r *http.Request, principal store.Principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal))
}

func requestPrincipal(r *http.Request) store.Principal {
	principal, _ := r.Context().Value(principalContextKey{}).(store.Principal)
	return principal
}

// sessionPrincipal resolves the administrator session cookie. The console has a
// single account, so a breakglass session is the only accepted identity.
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
