package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DefaultBaseURL is the public GitHub API. Tests point NewWithHTTP and
// ConvertManifest at an httptest server instead.
const DefaultBaseURL = "https://api.github.com"

// NewWithHTTP is New with an explicit API base URL and HTTP client. It exists
// for tests and local GitHub Enterprise deployments; production keeps using
// New and the public API.
func NewWithHTTP(cfg Config, baseURL string, client *http.Client) (*App, error) {
	app, err := New(cfg)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(baseURL) != "" {
		app.baseURL = strings.TrimRight(baseURL, "/")
	}
	if client != nil {
		app.http = client
	}
	return app, nil
}

// ID returns the App ID the client was built with.
func (a *App) ID() int64 { return a.id }

// AppInfo is the subset of GET /app the setup page needs. It carries no
// credential material.
type AppInfo struct {
	ID      int64  `json:"id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	HTMLURL string `json:"html_url"`
}

// Info reads the App's public identity. It requires the App JWT.
func (a *App) Info(ctx context.Context) (AppInfo, error) {
	var out AppInfo
	err := a.request(ctx, http.MethodGet, "/app", nil, &out)
	return out, err
}

// SetHookConfig points the App webhook at hookURL and rotates its secret.
// secret is sent to GitHub but never logged or returned by this method.
func (a *App) SetHookConfig(ctx context.Context, hookURL, secret string) error {
	if strings.TrimSpace(hookURL) == "" {
		return errors.New("GitHub webhook URL is required")
	}
	if secret == "" {
		return errors.New("GitHub webhook secret is required")
	}
	var out struct {
		URL string `json:"url"`
	}
	return a.request(ctx, http.MethodPatch, "/app/hook/config", map[string]any{
		"url":          hookURL,
		"content_type": "json",
		"secret":       secret,
	}, &out)
}

// Repository is one repository an App installation can access.
type Repository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

// InstallationRepositories lists every repository visible to any installation
// of this App. It uses a metadata-only discovery token per installation, so it
// never needs contents permission to enumerate.
func (a *App) InstallationRepositories(ctx context.Context) ([]Repository, error) {
	installations, err := a.ListInstallations(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	out := []Repository{}
	for _, installation := range installations {
		if installation.SuspendedAt != nil {
			continue
		}
		token, err := a.DiscoveryToken(ctx, installation.ID)
		if err != nil {
			return nil, err
		}
		for page := 1; page <= 10000; page++ {
			path := fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page)
			var result struct {
				TotalCount   int          `json:"total_count"`
				Repositories []Repository `json:"repositories"`
			}
			if err = a.requestWithToken(ctx, http.MethodGet, path, token.Token, &result); err != nil {
				return nil, err
			}
			for _, repository := range result.Repositories {
				if repository.ID <= 0 || repository.FullName == "" || seen[repository.ID] {
					continue
				}
				seen[repository.ID] = true
				out = append(out, repository)
			}
			if len(result.Repositories) < 100 {
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].FullName) < strings.ToLower(out[j].FullName) })
	return out, nil
}

// ManifestConversion is the credential-bearing response from
// POST /app-manifests/{code}/conversions. Its fields must never be logged or
// returned to a browser.
type ManifestConversion struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	HTMLURL       string `json:"html_url"`
	PEM           string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
}

// ConvertManifest exchanges the one-time code GitHub appends to the manifest
// callback for the created App's credentials. This endpoint is unauthenticated:
// the code itself is the capability, so callers must validate their own state
// before invoking it.
func ConvertManifest(ctx context.Context, client *http.Client, baseURL, code string) (ManifestConversion, error) {
	var out ManifestConversion
	code = strings.TrimSpace(code)
	if code == "" || len(code) > 200 || strings.ContainsAny(code, "/?#\\ \t\r\n") {
		return out, errors.New("invalid GitHub App manifest code")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/app-manifests/" + url.PathEscape(code) + "/conversions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	res, err := client.Do(req)
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return out, fmt.Errorf("GitHub App manifest conversion returned HTTP %d", res.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&out); err != nil {
		return out, errors.New("GitHub App manifest conversion returned invalid JSON")
	}
	if out.ID <= 0 || out.PEM == "" || out.WebhookSecret == "" {
		return out, errors.New("GitHub App manifest conversion omitted credentials")
	}
	return out, nil
}
