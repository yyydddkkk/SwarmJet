// Package githubapp signs App requests and issues short-lived installation credentials.
package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppID         int64
	PrivateKeyPEM []byte
}
type App struct {
	id      int64
	key     *rsa.PrivateKey
	baseURL string
	http    *http.Client
}
type Token struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Installation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
	SuspendedAt *time.Time `json:"suspended_at"`
}

func New(cfg Config) (*App, error) {
	if cfg.AppID <= 0 {
		return nil, errors.New("GitHub App ID must be positive")
	}
	block, _ := pem.Decode(cfg.PrivateKeyPEM)
	if block == nil {
		return nil, errors.New("GitHub App private key must be PEM")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, e := x509.ParsePKCS8PrivateKey(block.Bytes)
		if e != nil {
			return nil, errors.New("invalid GitHub App private key")
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("GitHub App private key must be RSA")
		}
	}
	return &App{id: cfg.AppID, key: key, baseURL: "https://api.github.com", http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (a *App) jwt() (string, error) {
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(a.id, 10)})
	enc := base64.RawURLEncoding.EncodeToString
	input := enc([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc(claims)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	return input + "." + enc(sig), err
}
func (a *App) request(ctx context.Context, method, path string, body, out any) error {
	token, err := a.jwt()
	if err != nil {
		return err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("Content-Type", "application/json")
	res, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub App API returned HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}
func (a *App) GetInstallation(ctx context.Context, id int64) (Installation, error) {
	var out Installation
	if id <= 0 {
		return out, errors.New("installation ID must be positive")
	}
	err := a.request(ctx, "GET", fmt.Sprintf("/app/installations/%d", id), nil, &out)
	if err == nil && (out.ID != id || out.SuspendedAt != nil) {
		err = errors.New("GitHub installation is unavailable")
	}
	return out, err
}
func (a *App) ListInstallations(ctx context.Context) ([]Installation, error) {
	var all []Installation
	for page := 1; page <= 10000; page++ {
		var rows []Installation
		err := a.request(ctx, "GET", fmt.Sprintf("/app/installations?per_page=100&page=%d", page), nil, &rows)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if len(rows) < 100 {
			return all, nil
		}
	}
	return nil, errors.New("GitHub installation pagination exceeds limit")
}

// InstallationToken scopes credentials to explicit repositories. DiscoveryToken is used only to enumerate installation repositories.
func (a *App) InstallationToken(ctx context.Context, id int64, repositoryIDs []int64) (Token, error) {
	if len(repositoryIDs) == 0 {
		return Token{}, errors.New("repository scope is required")
	}
	for _, v := range repositoryIDs {
		if v <= 0 {
			return Token{}, errors.New("invalid repository scope")
		}
	}
	return a.token(ctx, id, map[string]any{"repository_ids": repositoryIDs})
}
func (a *App) DiscoveryToken(ctx context.Context, id int64) (Token, error) {
	return a.token(ctx, id, map[string]any{"permissions": map[string]string{"metadata": "read"}})
}
func (a *App) token(ctx context.Context, id int64, body any) (Token, error) {
	var out Token
	if id <= 0 {
		return out, errors.New("installation ID must be positive")
	}
	err := a.request(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", id), body, &out)
	if err == nil && (out.Token == "" || !out.ExpiresAt.After(time.Now())) {
		return Token{}, errors.New("GitHub returned invalid installation token")
	}
	return out, err
}
func VerifyWebhook(secret string, body []byte, signature string) bool {
	if secret == "" || !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	actual, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(actual, mac.Sum(nil))
}

// ReadOnlyInstallationToken grants dependency inspection for explicit repositories
// in this installation without granting any Git or issue write operation.
func (a *App) ReadOnlyInstallationToken(ctx context.Context, id int64, repositoryIDs []int64) (Token, error) {
	if len(repositoryIDs) == 0 {
		return Token{}, errors.New("dependency repository scope is required")
	}
	for _, repositoryID := range repositoryIDs {
		if repositoryID <= 0 {
			return Token{}, errors.New("invalid dependency repository scope")
		}
	}
	return a.token(ctx, id, map[string]any{"repository_ids": repositoryIDs, "permissions": map[string]string{"metadata": "read", "issues": "read"}})
}

// ReadOnlyRepositoryToken resolves a repository name within one installation.
// Verifying the owner prevents a same-name repository in another account from
// accidentally being used for a cross-installation dependency.
func (a *App) ReadOnlyRepositoryToken(ctx context.Context, id int64, fullName string) (Token, error) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(fullName, "?#\\") {
		return Token{}, errors.New("invalid dependency repository name")
	}
	installation, err := a.GetInstallation(ctx, id)
	if err != nil {
		return Token{}, err
	}
	if !strings.EqualFold(installation.Account.Login, parts[0]) {
		return Token{}, errors.New("GitHub dependency repository belongs to another installation account")
	}
	return a.token(ctx, id, map[string]any{"repositories": []string{parts[1]}, "permissions": map[string]string{"metadata": "read", "issues": "read"}})
}
