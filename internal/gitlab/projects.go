package gitlab

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// User is the verified identity behind a token.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

// CurrentUser verifies that a token is accepted by this exact GitLab origin.
// The client is constructed with the configured base URL and never follows a
// redirect, so the token cannot be replayed to another origin.
func (c *Client) CurrentUser(ctx context.Context) (User, error) {
	var u User
	if c.token == "" {
		return u, errors.New("token is empty")
	}
	_, err := c.request(ctx, "GET", "/user", nil, &u)
	if err != nil {
		return u, err
	}
	if u.ID <= 0 || strings.TrimSpace(u.Username) == "" {
		return u, errors.New("GitLab returned an incomplete user identity")
	}
	return u, nil
}

// ProjectInfo is the metadata the console needs to select a repository.
type ProjectInfo struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	DefaultBranch     string `json:"default_branch"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
	WebURL            string `json:"web_url"`
	Archived          bool   `json:"archived"`
	Description       string `json:"description"`
}

// ProjectPage is one bounded page of accessible repositories.
type ProjectPage struct {
	Projects []ProjectInfo `json:"projects"`
	Page     int           `json:"page"`
	PerPage  int           `json:"perPage"`
	HasNext  bool          `json:"hasNext"`
	HasPrev  bool          `json:"hasPrev"`
}

// ListProjects returns repositories the token can access, with real GitLab IDs,
// repository URLs and default branches. Search and pagination are delegated to
// GitLab so large instances stay responsive.
func (c *Client) ListProjects(ctx context.Context, search string, page, perPage int) (ProjectPage, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 50
	}
	search = strings.TrimSpace(search)
	if len(search) > 200 {
		search = search[:200]
	}
	q := url.Values{
		"membership": {"true"},
		"simple":     {"true"},
		"order_by":   {"last_activity_at"},
		"sort":       {"desc"},
		"page":       {strconv.Itoa(page)},
		"per_page":   {strconv.Itoa(perPage)},
	}
	if search != "" {
		q.Set("search", search)
	}
	var rows []ProjectInfo
	headers, err := c.request(ctx, "GET", "/projects?"+q.Encode(), nil, &rows)
	if err != nil {
		return ProjectPage{}, err
	}
	out := ProjectPage{Projects: rows, Page: page, PerPage: perPage, HasNext: headers.Get("X-Next-Page") != "", HasPrev: headers.Get("X-Prev-Page") != ""}
	if out.Projects == nil {
		out.Projects = []ProjectInfo{}
	}
	return out, nil
}

// StatusCode extracts the HTTP status from a client error without exposing the
// response body, which may echo request content.
func StatusCode(err error) (int, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode, true
	}
	return 0, false
}
