// Package github implements the GitHub.com hosting adapter.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TokenSource func(context.Context) (string, error)

// DependencyTokenSource issues read-only, per-repository credentials.
// Exactly one of repositoryID and fullName is populated.
type DependencyTokenSource func(ctx context.Context, repositoryID int64, fullName string) (string, error)
type Client struct {
	baseURL         string
	token           TokenSource
	http            *http.Client
	names           sync.Map
	notes           sync.Mutex
	mergeMethod     string
	dependencyToken DependencyTokenSource
}

func New(source TokenSource) *Client {
	return &Client{baseURL: "https://api.github.com", token: source, mergeMethod: "merge", http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *Client) SetMergeMethod(method string) error {
	if method != "merge" && method != "squash" && method != "rebase" {
		return errors.New("invalid GitHub merge method")
	}
	c.mergeMethod = method
	return nil
}

type APIError struct{ StatusCode int }

func (e *APIError) Error() string { return fmt.Sprintf("GitHub API returned HTTP %d", e.StatusCode) }
func StatusCode(err error) (int, bool) {
	var e *APIError
	if errors.As(err, &e) {
		return e.StatusCode, true
	}
	return 0, false
}
func (c *Client) request(ctx context.Context, method, path string, body, out any) (http.Header, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("invalid GitHub API path")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	if token == "" {
		return nil, errors.New("empty GitHub token")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, &APIError{res.StatusCode}
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, errors.New("GitHub response exceeds limit")
	}
	if out != nil && len(data) > 0 {
		err = json.Unmarshal(data, out)
	}
	return res.Header, err
}
func paginate[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("per_page", "100")
	var all []T
	for page := 1; page <= 10000; page++ {
		q.Set("page", strconv.Itoa(page))
		var rows []T
		h, err := c.request(ctx, "GET", path+"?"+q.Encode(), nil, &rows)
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if !strings.Contains(h.Get("Link"), `rel="next"`) {
			return all, nil
		}
	}
	return nil, errors.New("GitHub pagination exceeds limit")
}

type Repository struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	FullName         string `json:"full_name"`
	DefaultBranch    string `json:"default_branch"`
	CloneURL         string `json:"clone_url"`
	HTMLURL          string `json:"html_url"`
	Archived         bool   `json:"archived"`
	Description      string `json:"description"`
	AllowMergeCommit bool   `json:"allow_merge_commit"`
	AllowSquashMerge bool   `json:"allow_squash_merge"`
	AllowRebaseMerge bool   `json:"allow_rebase_merge"`
}

func (c *Client) GetRepository(ctx context.Context, id int64) (Repository, error) {
	var r Repository
	_, err := c.request(ctx, "GET", fmt.Sprintf("/repositories/%d", id), nil, &r)
	if err == nil && (r.ID != id || len(strings.Split(r.FullName, "/")) != 2) {
		err = errors.New("invalid GitHub repository")
	}
	if err == nil {
		c.names.Store(id, r.FullName)
	}
	return r, err
}
func (c *Client) repo(ctx context.Context, id int64) (string, error) {
	if name, ok := c.names.Load(id); ok {
		return "/repos/" + name.(string), nil
	}
	r, err := c.GetRepository(ctx, id)
	return "/repos/" + r.FullName, err
}

// SetRepositoryName uses the already validated Hive repository name for
// read-only list calls, avoiding an extra lookup for every short-lived App client.
func (c *Client) SetRepositoryName(id int64, name string) {
	parts := strings.Split(name, "/")
	if id > 0 && len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.ContainsAny(name, "?#%\\") {
		c.names.Store(id, name)
	}
}
func (c *Client) ListRepositories(ctx context.Context) ([]Repository, error) {
	var all []Repository
	for page := 1; page <= 10000; page++ {
		var out struct {
			Repositories []Repository `json:"repositories"`
		}
		h, err := c.request(ctx, "GET", fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), nil, &out)
		if err != nil {
			return nil, err
		}
		for _, r := range out.Repositories {
			c.names.Store(r.ID, r.FullName)
		}
		all = append(all, out.Repositories...)
		if !strings.Contains(h.Get("Link"), `rel="next"`) {
			return all, nil
		}
	}
	return nil, errors.New("GitHub pagination exceeds limit")
}
func (c *Client) graphql(ctx context.Context, query string, variables any, out any) error {
	var response struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	_, err := c.request(ctx, "POST", "/graphql", map[string]any{"query": query, "variables": variables}, &response)
	if err != nil {
		return err
	}
	if len(response.Errors) > 0 {
		return errors.New("GitHub GraphQL request failed")
	}
	return json.Unmarshal(response.Data, out)
}

func (c *Client) SetDependencyTokenSource(source DependencyTokenSource) { c.dependencyToken = source }
func (c *Client) dependencyReader(id int64, name string) (*Client, error) {
	if c.dependencyToken == nil {
		return nil, errors.New("GitHub dependency repository is outside the project credential scope; configure read-only dependency access for the same installation")
	}
	reader := New(func(ctx context.Context) (string, error) {
		token, err := c.dependencyToken(ctx, id, name)
		if err != nil {
			return "", fmt.Errorf("GitHub dependency repository is inaccessible to this installation: %w", err)
		}
		return token, nil
	})
	reader.baseURL = c.baseURL
	reader.http = c.http
	if id > 0 && name != "" {
		reader.names.Store(id, name)
	}
	return reader, nil
}
func dependencyScopeError(err error) bool {
	code, ok := StatusCode(err)
	return ok && (code == 403 || code == 404)
}
