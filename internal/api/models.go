package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/llmproxy"
)

const maxUsageFilter = 200

// models routes the console's model catalog and credential endpoints. Every
// request here is already authenticated as the administrator session.
func (a *server) models(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/models":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			problem(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.listModels(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/models/credentials/"):
		provider := strings.TrimPrefix(r.URL.Path, "/api/models/credentials/")
		if provider == "" || strings.Contains(provider, "/") || !llmproxy.ValidProvider(provider) {
			problem(w, http.StatusNotFound, "API route not found")
			return
		}
		switch r.Method {
		case http.MethodPut:
			a.putModelCredential(w, r, provider)
		case http.MethodDelete:
			a.deleteModelCredential(w, r, provider)
		default:
			problem(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		problem(w, http.StatusNotFound, "API route not found")
	}
}

type modelCredentialView struct {
	Provider   string `json:"provider"`
	Type       string `json:"type,omitempty"`
	Masked     string `json:"masked,omitempty"`
	Configured bool   `json:"configured"`
}

type modelsResponse struct {
	DefaultModel     string                `json:"defaultModel"`
	Models           []llmproxy.Model      `json:"models"`
	Providers        []modelCredentialView `json:"providers"`
	GatewayAvailable bool                  `json:"gatewayAvailable"`
}

func (a *server) listModels(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Models == nil {
		problem(w, http.StatusServiceUnavailable, "model services are not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	providers := []modelCredentialView{}
	if credentials, err := a.cfg.Models.Snapshot(ctx); err == nil {
		byProvider := map[string]modelCredentialView{}
		for _, credential := range credentials {
			current, exists := byProvider[credential.Provider]
			// A masked API key is more useful than an OAuth marker when both
			// exist; otherwise keep the first row.
			if !exists || (current.Masked == "" && credential.Masked != "") {
				byProvider[credential.Provider] = modelCredentialView{
					Provider:   credential.Provider,
					Type:       credential.Type,
					Masked:     credential.Masked,
					Configured: credential.Configured,
				}
			}
		}
		for _, credential := range byProvider {
			providers = append(providers, credential)
		}
		sort.Slice(providers, func(i, j int) bool { return providers[i].Provider < providers[j].Provider })
	}

	models := []llmproxy.Model{}
	gatewayAvailable := false
	if catalog, err := a.cfg.Models.Models(ctx); err == nil {
		models = catalog
		gatewayAvailable = true
	}
	defaultModel := strings.TrimSpace(a.cfg.DefaultModel)
	if defaultModel == "" {
		defaultModel = llmproxy.DefaultModel
	}
	respond(w, http.StatusOK, modelsResponse{
		DefaultModel:     defaultModel,
		Models:           models,
		Providers:        providers,
		GatewayAvailable: gatewayAvailable,
	})
}

func (a *server) putModelCredential(w http.ResponseWriter, r *http.Request, provider string) {
	if a.cfg.Models == nil {
		problem(w, http.StatusServiceUnavailable, "model services are not configured")
		return
	}
	var req struct {
		APIKey string `json:"apiKey"`
		Key    string `json:"key"`
	}
	if !decode(w, r, &req) {
		return
	}
	key := req.APIKey
	if key == "" {
		key = req.Key
	}
	if err := llmproxy.ValidateAPIKey(key); err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	credential, err := a.cfg.Models.SetAPIKey(ctx, provider, key)
	if err != nil {
		if errors.Is(err, llmproxy.ErrNotConfigured) {
			problem(w, http.StatusServiceUnavailable, "model credential service is not configured")
			return
		}
		// Never echo the broker response or the submitted key.
		problem(w, http.StatusBadGateway, "could not store the model credential")
		return
	}
	respond(w, http.StatusOK, modelCredentialView{
		Provider:   credential.Provider,
		Type:       credential.Type,
		Masked:     credential.Masked,
		Configured: credential.Configured,
	})
}

func (a *server) deleteModelCredential(w http.ResponseWriter, r *http.Request, provider string) {
	if a.cfg.Models == nil {
		problem(w, http.StatusServiceUnavailable, "model services are not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := a.cfg.Models.DeleteProvider(ctx, provider); err != nil {
		switch {
		case errors.Is(err, llmproxy.ErrCredentialNotFound):
			problem(w, http.StatusNotFound, "model credential not found")
		case errors.Is(err, llmproxy.ErrNotConfigured):
			problem(w, http.StatusServiceUnavailable, "model credential service is not configured")
		default:
			problem(w, http.StatusBadGateway, "could not remove the model credential")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// usage implements GET /api/usage?hive=&agent=.
func (a *server) usage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	hiveID := strings.TrimSpace(r.URL.Query().Get("hive"))
	agentID := strings.TrimSpace(r.URL.Query().Get("agent"))
	if len(hiveID) > maxUsageFilter || len(agentID) > maxUsageFilter {
		problem(w, http.StatusBadRequest, "usage filter is too long")
		return
	}
	summary, err := a.s.LLMUsageSummary(r.Context(), hiveID, agentID)
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, http.StatusOK, summary)
}
