// Package service receives GitHub webhook deliveries and hands them to the
// deterministic Router. The retired GitLab, issue-scanning and pipeline code
// no longer exists: a repository without a Hive is ignored and logged.
package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/router"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// Service accepts GitHub deliveries and routes them to Hives.
type Service struct {
	Store *store.Store
	// Webhooks records the delivery and enqueues its River job in one
	// transaction. *store.Store implements it in production.
	Webhooks interface {
		AcceptWebhook(context.Context, string, string, string, []byte) error
	}

	// HiveRouter optionally injects a Router (tests); it must be set before the
	// Service starts handling deliveries. hiveRouter reads it only inside the
	// Once and stores the result in hiveRoutes.
	HiveRouter     *router.Router
	hiveRouterOnce sync.Once
	hiveRoutes     *router.Router
}

var ErrInvalidWebhookDelivery = errors.New("invalid webhook delivery")

// GitHubWebhook verifies the raw request body before queueing the delivery.
// Only GitHub is supported in round 3.
func (s *Service) GitHubWebhook(secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if secret == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		got := r.Header.Get("X-Hub-Signature-256")
		if !hmac.Equal([]byte(want), []byte(got)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !s.acceptWebhook(w, r, "github", deliveryID(r.Header.Get("X-GitHub-Delivery")), r.Header.Get("X-GitHub-Event"), body) {
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
}

func deliveryID(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate != "" && len(candidate) <= 200 && strings.TrimSpace(candidate) == candidate {
			return candidate
		}
	}
	return ""
}

func (s *Service) acceptWebhook(w http.ResponseWriter, r *http.Request, provider, id, eventType string, body []byte) bool {
	if id == "" {
		http.Error(w, "missing delivery ID", http.StatusBadRequest)
		return false
	}
	if s.Webhooks == nil {
		http.Error(w, "webhook queue unavailable", http.StatusServiceUnavailable)
		return false
	}
	if len(eventType) > 200 {
		http.Error(w, "event type too long", http.StatusBadRequest)
		return false
	}
	publishCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	err := s.Webhooks.AcceptWebhook(publishCtx, provider, id, eventType, body)
	if errors.Is(err, store.ErrWebhookConflict) {
		slog.Error("webhook delivery ID content mismatch", "provider", provider, "delivery_id", id, "error", err)
		http.Error(w, "delivery conflict", http.StatusConflict)
		return false
	}
	if err != nil {
		slog.Error("webhook queue", "provider", provider, "delivery_id", id, "error", err)
		http.Error(w, "webhook queue unavailable", http.StatusServiceUnavailable)
		return false
	}
	slog.Info("webhook queued", "provider", provider, "delivery_id", id)
	return true
}
