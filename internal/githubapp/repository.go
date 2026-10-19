package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// RepositoryInstallation resolves the installation that grants this App access
// to a single repository. GitHub only returns an installation the App can see,
// so the result is also the authorization check for the repository.
//
//	GET /repos/{owner}/{repo}/installation
func (a *App) RepositoryInstallation(ctx context.Context, fullName string) (Installation, error) {
	owner, name, err := splitRepository(fullName)
	if err != nil {
		return Installation{}, err
	}
	var out Installation
	err = a.request(ctx, http.MethodGet, "/repos/"+owner+"/"+name+"/installation", nil, &out)
	if err != nil {
		return Installation{}, err
	}
	if out.ID <= 0 || out.SuspendedAt != nil {
		return Installation{}, errors.New("GitHub installation is unavailable for this repository")
	}
	return out, nil
}

// RepositoryToken mints an installation token scoped to exactly one repository
// with the App's configured permissions. The token is short-lived and must stay
// in memory; callers never log it.
func (a *App) RepositoryToken(ctx context.Context, fullName string) (Token, error) {
	owner, name, err := splitRepository(fullName)
	if err != nil {
		return Token{}, err
	}
	installation, err := a.RepositoryInstallation(ctx, fullName)
	if err != nil {
		return Token{}, err
	}
	if !strings.EqualFold(installation.Account.Login, owner) {
		return Token{}, errors.New("GitHub repository belongs to another installation account")
	}
	return a.token(ctx, installation.ID, map[string]any{"repositories": []string{name}})
}

// RepositoryID resolves the numeric repository ID that GitHub uses in API
// paths. The lookup itself is authenticated with a token scoped to the same
// single repository.
func (a *App) RepositoryID(ctx context.Context, fullName string) (int64, error) {
	owner, name, err := splitRepository(fullName)
	if err != nil {
		return 0, err
	}
	token, err := a.RepositoryToken(ctx, fullName)
	if err != nil {
		return 0, err
	}
	var repo struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	if err = a.requestWithToken(ctx, http.MethodGet, "/repos/"+owner+"/"+name, token.Token, &repo); err != nil {
		return 0, err
	}
	if repo.ID <= 0 || !strings.EqualFold(repo.FullName, fullName) {
		return 0, errors.New("GitHub returned an unexpected repository")
	}
	return repo.ID, nil
}

// splitRepository validates owner/name before it becomes part of an API path.
func splitRepository(fullName string) (string, string, error) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("invalid GitHub repository name")
	}
	owner, name := parts[0], parts[1]
	if strings.ContainsAny(fullName, "?#\\ \t\r\n") ||
		strings.ContainsAny(owner, "?#\\ \t\r\n") ||
		strings.ContainsAny(name, "?#\\ \t\r\n") {
		return "", "", errors.New("invalid GitHub repository name")
	}
	return owner, name, nil
}

// requestWithToken performs an installation-token request. It deliberately
// does not reuse request: that one always signs with the App JWT.
func (a *App) requestWithToken(ctx context.Context, method, path, token string, out any) error {
	if token == "" {
		return errors.New("empty GitHub installation token")
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	res, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("GitHub API returned HTTP %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}
