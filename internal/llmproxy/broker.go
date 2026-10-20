// Package llmproxy is the platform's model boundary: it authenticates Agent
// tokens at /llm, enforces methodology budgets, records usage, and talks to the
// omp auth broker/gateway. Provider credentials never enter a Cell and never
// appear in this package's logs or API responses.
package llmproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// DefaultModel is the model used when SWARMJET_DEFAULT_MODEL is unset.
const DefaultModel = "deepseek/deepseek-chat"

var (
	// ErrNotConfigured means the auth broker or gateway endpoint/token is absent.
	ErrNotConfigured = errors.New("model credential service is not configured")
	// ErrCredentialNotFound means the provider has no stored credential.
	ErrCredentialNotFound = errors.New("model credential not found")
)

var providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidProvider reports whether a provider id is safe to put on a broker URL
// path or in the console.
func ValidProvider(provider string) bool { return providerPattern.MatchString(provider) }

// ValidateAPIKey rejects empty, oversized or control-character keys. It never
// includes the key in the returned error.
func ValidateAPIKey(key string) error {
	if key == "" {
		return errors.New("api key is required")
	}
	if key != strings.TrimSpace(key) {
		return errors.New("api key must not have leading or trailing whitespace")
	}
	if len(key) > 4096 {
		return errors.New("api key is too long")
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return errors.New("api key contains control characters")
		}
	}
	return nil
}

// MaskSecret returns a display value that never contains the full secret.
func MaskSecret(secret string) string {
	if len(secret) < 8 {
		return "********"
	}
	return "••••" + secret[len(secret)-4:]
}

// Credential is the console-safe view of one stored provider credential.
type Credential struct {
	Provider   string `json:"provider"`
	Type       string `json:"type"`
	Masked     string `json:"masked,omitempty"`
	Configured bool   `json:"configured"`
}

// Model is one model the gateway advertises.
type Model struct {
	ID              string `json:"id"`
	Provider        string `json:"provider"`
	DisplayName     string `json:"displayName,omitempty"`
	ContextLength   int    `json:"contextLength,omitempty"`
	MaxOutputTokens int    `json:"maxOutputTokens,omitempty"`
	Kind            string `json:"kind,omitempty"`
}

// BrokerConfig points at the auth broker (credential writes) and the auth
// gateway (model catalog and inference). Token files are read per call so a
// rotation by deploy/install.py takes effect without a restart.
type BrokerConfig struct {
	BrokerURL        string
	BrokerTokenFile  string
	GatewayURL       string
	GatewayTokenFile string
	HTTPClient       *http.Client
}

// BrokerClient is the control-plane client for the omp auth broker and gateway.
type BrokerClient struct {
	brokerURL        string
	brokerTokenFile  string
	gatewayURL       string
	gatewayTokenFile string
	http             *http.Client
}

// NewBrokerClient validates the endpoints. Missing token files are not an
// error here; calls return ErrNotConfigured until deploy/install.py writes them.
func NewBrokerClient(cfg BrokerConfig) (*BrokerClient, error) {
	brokerURL, err := normalizeBaseURL(cfg.BrokerURL)
	if err != nil {
		return nil, fmt.Errorf("auth broker URL: %w", err)
	}
	gatewayURL, err := normalizeBaseURL(cfg.GatewayURL)
	if err != nil {
		return nil, fmt.Errorf("auth gateway URL: %w", err)
	}
	if brokerURL == "" && gatewayURL == "" {
		return nil, ErrNotConfigured
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &BrokerClient{
		brokerURL:        brokerURL,
		brokerTokenFile:  cfg.BrokerTokenFile,
		gatewayURL:       gatewayURL,
		gatewayTokenFile: cfg.GatewayTokenFile,
		http:             client,
	}, nil
}

func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("must be an http(s) URL")
	}
	return raw, nil
}

func readSecretFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", ErrNotConfigured
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", ErrNotConfigured
	}
	return secret, nil
}

func (c *BrokerClient) request(ctx context.Context, method, endpoint, token string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Never include the broker/gateway response body: it can contain a key.
		return fmt.Errorf("upstream returned %s", resp.Status)
	}
	if out != nil && len(payload) > 0 {
		if err = json.Unmarshal(payload, out); err != nil {
			return fmt.Errorf("decode upstream response: %w", err)
		}
	}
	return nil
}

type snapshotEntry struct {
	ID         int64  `json:"id"`
	Provider   string `json:"provider"`
	Identity   string `json:"identityKey"`
	Credential struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	} `json:"credential"`
}

type snapshotResponse struct {
	Credentials []snapshotEntry `json:"credentials"`
}

// Snapshot lists the broker's credentials. API keys are immediately masked and
// the raw value is not retained in the returned slice.
func (c *BrokerClient) Snapshot(ctx context.Context) ([]Credential, error) {
	if c.brokerURL == "" {
		return nil, ErrNotConfigured
	}
	token, err := readSecretFile(c.brokerTokenFile)
	if err != nil {
		return nil, err
	}
	var response snapshotResponse
	if err = c.request(ctx, http.MethodGet, c.brokerURL+"/v1/snapshot", token, nil, &response); err != nil {
		return nil, err
	}
	out := make([]Credential, 0, len(response.Credentials))
	for _, entry := range response.Credentials {
		credential := Credential{
			Provider:   entry.Provider,
			Type:       entry.Credential.Type,
			Configured: entry.Credential.Type == "api_key" || entry.Credential.Type == "oauth",
		}
		if entry.Credential.Type == "api_key" {
			credential.Masked = MaskSecret(entry.Credential.Key)
		}
		out = append(out, credential)
	}
	return out, nil
}

// SetAPIKey upserts one provider API key into the broker. The key is written
// only to the broker; the response value is masked.
func (c *BrokerClient) SetAPIKey(ctx context.Context, provider, key string) (Credential, error) {
	if !ValidProvider(provider) {
		return Credential{}, errors.New("invalid provider")
	}
	if err := ValidateAPIKey(key); err != nil {
		return Credential{}, err
	}
	if c.brokerURL == "" {
		return Credential{}, ErrNotConfigured
	}
	token, err := readSecretFile(c.brokerTokenFile)
	if err != nil {
		return Credential{}, err
	}
	payload := map[string]any{
		"provider": provider,
		"credential": map[string]any{
			"type": "api_key",
			"key":  key,
		},
	}
	var response struct {
		Entries []snapshotEntry `json:"entries"`
	}
	if err = c.request(ctx, http.MethodPost, c.brokerURL+"/v1/credential", token, payload, &response); err != nil {
		return Credential{}, err
	}
	return Credential{
		Provider:   provider,
		Type:       "api_key",
		Masked:     MaskSecret(key),
		Configured: true,
	}, nil
}

// SetModelCredential is the MCP-facing shape of SetAPIKey: it returns only the
// masked display value.
func (c *BrokerClient) SetModelCredential(ctx context.Context, provider, key string) (string, error) {
	credential, err := c.SetAPIKey(ctx, provider, key)
	if err != nil {
		return "", err
	}
	return credential.Masked, nil
}

// DeleteProvider disables every broker credential for one provider. The broker
// has no hard-delete endpoint, and a disabled tombstone is its documented
// removal semantics.
func (c *BrokerClient) DeleteProvider(ctx context.Context, provider string) error {
	if !ValidProvider(provider) {
		return errors.New("invalid provider")
	}
	if c.brokerURL == "" {
		return ErrNotConfigured
	}
	token, err := readSecretFile(c.brokerTokenFile)
	if err != nil {
		return err
	}
	var response snapshotResponse
	if err = c.request(ctx, http.MethodGet, c.brokerURL+"/v1/snapshot", token, nil, &response); err != nil {
		return err
	}
	found := false
	for _, entry := range response.Credentials {
		if entry.Provider != provider {
			continue
		}
		found = true
		endpoint := fmt.Sprintf("%s/v1/credential/%d/disable", c.brokerURL, entry.ID)
		if err = c.request(ctx, http.MethodPost, endpoint, token,
			map[string]string{"cause": "removed from the SwarmJet console"}, nil); err != nil {
			return err
		}
	}
	if !found {
		return ErrCredentialNotFound
	}
	return nil
}

// Models lists the gateway's served catalog. The gateway filters this list to
// providers it holds credentials for.
func (c *BrokerClient) Models(ctx context.Context) ([]Model, error) {
	if c.gatewayURL == "" {
		return nil, ErrNotConfigured
	}
	token, err := readSecretFile(c.gatewayTokenFile)
	if err != nil {
		return nil, err
	}
	var response struct {
		Data []struct {
			ID              string `json:"id"`
			OwnedBy         string `json:"owned_by"`
			DisplayName     string `json:"display_name"`
			ContextLength   int    `json:"context_length"`
			MaxOutputTokens int    `json:"max_output_tokens"`
			Kind            string `json:"kind"`
		} `json:"data"`
	}
	if err = c.request(ctx, http.MethodGet, c.gatewayURL+"/v1/models", token, nil, &response); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(response.Data))
	for _, row := range response.Data {
		model := Model{
			ID:              row.ID,
			Provider:        row.OwnedBy,
			DisplayName:     row.DisplayName,
			ContextLength:   row.ContextLength,
			MaxOutputTokens: row.MaxOutputTokens,
			Kind:            row.Kind,
		}
		if model.Provider == "" {
			if slash := strings.IndexByte(row.ID, '/'); slash > 0 {
				model.Provider = row.ID[:slash]
			}
		}
		models = append(models, model)
	}
	return models, nil
}
