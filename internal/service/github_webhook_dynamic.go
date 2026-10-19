package service

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/yyydddkkk/SwarmJet/internal/githubapp"
)

// GitHubWebhookSecretSource resolves the current webhook secret. The Provider
// implements it.
type GitHubWebhookSecretSource func(context.Context) (string, error)

// GitHubWebhookDynamic verifies a delivery with the App's current webhook
// secret instead of a value captured at startup. An App created on the setup
// page therefore starts verifying deliveries without a restart.
func (s *Service) GitHubWebhookDynamic(source GitHubWebhookSecretSource) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		if source == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		secret, err := source(r.Context())
		if err != nil || secret == "" {
			slog.Warn("GitHub webhook rejected: App webhook secret is unavailable")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !githubapp.VerifyWebhook(secret, body, r.Header.Get("X-Hub-Signature-256")) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !s.acceptWebhook(w, r, "github", deliveryID(r.Header.Get("X-GitHub-Delivery")), r.Header.Get("X-GitHub-Event"), body) {
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
}
