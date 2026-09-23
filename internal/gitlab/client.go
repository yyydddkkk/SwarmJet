// Package gitlab provides a credential-scoped GitLab REST client.
package gitlab

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"swarmjet/internal/domain"
	"sync"
	"time"
)

type Client struct {
	baseURL, token string
	http           *http.Client
	notes          sync.Mutex
}

func New(baseURL, token string) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(baseURL, "/api/v4") {
		baseURL += "/api/v4"
	}
	return &Client{baseURL: baseURL, token: token, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type APIError struct{ StatusCode int }

func (e *APIError) Error() string { return fmt.Sprintf("GitLab API returned HTTP %d", e.StatusCode) }
func (c *Client) request(ctx context.Context, method, path string, body, out any) (http.Header, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{resp.StatusCode}
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8*1024*1024 {
		return nil, errors.New("GitLab response exceeds size limit")
	}
	if out != nil {
		if err = json.Unmarshal(data, out); err != nil {
			return nil, fmt.Errorf("decode GitLab response: %w", err)
		}
	}
	return resp.Header, nil
}
func paginate[T any](ctx context.Context, c *Client, path string, q url.Values) ([]T, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("per_page", "100")
	page := 1
	var all []T
	for {
		q.Set("page", strconv.Itoa(page))
		var items []T
		h, err := c.request(ctx, "GET", path+"?"+q.Encode(), nil, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		next := h.Get("X-Next-Page")
		if next == "" {
			if len(items) < 100 {
				return all, nil
			}
			page++
		} else {
			n, e := strconv.Atoi(next)
			if e != nil || n <= page {
				return nil, errors.New("invalid GitLab pagination")
			}
			page = n
		}
		if page > 10000 {
			return nil, errors.New("GitLab pagination exceeds limit")
		}
	}
}
func project(id int64) string { return fmt.Sprintf("/projects/%d", id) }
func issuePath(i domain.Issue) string {
	return fmt.Sprintf("%s/issues/%d", project(i.ProjectID), i.IID)
}

type apiIssue struct {
	ProjectID   int64    `json:"project_id"`
	IID         int64    `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	URL         string   `json:"web_url"`
	State       string   `json:"state"`
	Labels      []string `json:"labels"`
}

func (i apiIssue) domain(fallback int64) domain.Issue {
	if i.ProjectID == 0 {
		i.ProjectID = fallback
	}
	return domain.Issue{ProjectID: i.ProjectID, IID: i.IID, Title: i.Title, Description: i.Description, URL: i.URL, State: i.State, Labels: i.Labels}
}
func (c *Client) ListEligibleIssues(ctx context.Context, projectID int64) ([]domain.Issue, error) {
	rows, err := paginate[apiIssue](ctx, c, project(projectID)+"/issues", url.Values{"state": {"opened"}, "labels": {"autonomous"}, "order_by": {"created_at"}, "sort": {"asc"}})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Issue, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.domain(projectID))
	}
	return out, nil
}
func (c *Client) GetIssue(ctx context.Context, projectID, iid int64) (domain.Issue, error) {
	var out apiIssue
	_, err := c.request(ctx, "GET", issuePath(domain.Issue{ProjectID: projectID, IID: iid}), nil, &out)
	return out.domain(projectID), err
}
func (c *Client) Dependencies(ctx context.Context, issue domain.Issue) ([]domain.IssueRef, error) {
	rows, err := paginate[struct {
		ProjectID int64  `json:"project_id"`
		IID       int64  `json:"iid"`
		LinkType  string `json:"link_type"`
	}](ctx, c, issuePath(issue)+"/links", nil)
	if err != nil {
		return nil, err
	}
	out := []domain.IssueRef{}
	for _, r := range rows {
		if r.LinkType == "is_blocked_by" {
			out = append(out, domain.IssueRef{ProjectID: r.ProjectID, IID: r.IID})
		}
	}
	return out, nil
}

type Branch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Commit    struct {
		ID string `json:"id"`
	} `json:"commit"`
}

func (c *Client) GetBranch(ctx context.Context, projectID int64, name string) (Branch, error) {
	var b Branch
	_, err := c.request(ctx, "GET", project(projectID)+"/repository/branches/"+url.PathEscape(name), nil, &b)
	return b, err
}
func (c *Client) ListBranches(ctx context.Context, projectID int64) ([]Branch, error) {
	return paginate[Branch](ctx, c, project(projectID)+"/repository/branches", nil)
}
func (c *Client) EnsureTarget(ctx context.Context, repo domain.Repository) (string, error) {
	if repo.TargetBranch == "" {
		return "", errors.New("target branch is required")
	}
	b, err := c.GetBranch(ctx, repo.ProjectID, repo.TargetBranch)
	if err == nil {
		return branchSHA(b)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		return "", err
	}
	if repo.CreateFrom == "" {
		return "", errors.New("missing target branch requires explicit createFrom")
	}
	// Resolve the explicit ref first so a concurrent creator cannot silently choose a different base.
	var commit struct {
		ID string `json:"id"`
	}
	_, err = c.request(ctx, "GET", project(repo.ProjectID)+"/repository/commits/"+url.PathEscape(repo.CreateFrom), nil, &commit)
	if err != nil {
		return "", err
	}
	if commit.ID == "" {
		return "", errors.New("createFrom resolved to empty SHA")
	}
	_, err = c.request(ctx, "POST", project(repo.ProjectID)+"/repository/branches", map[string]string{"branch": repo.TargetBranch, "ref": commit.ID}, &b)
	if err != nil {
		if !errors.As(err, &apiErr) || (apiErr.StatusCode != 400 && apiErr.StatusCode != 409) {
			return "", err
		}
		b, err = c.GetBranch(ctx, repo.ProjectID, repo.TargetBranch)
		if err != nil {
			return "", err
		}
	}
	if b.Name != repo.TargetBranch || b.Commit.ID != commit.ID {
		return "", errors.New("created target branch does not match explicit base")
	}
	return branchSHA(b)
}
func branchSHA(b Branch) (string, error) {
	if b.Commit.ID == "" {
		return "", errors.New("branch has no commit SHA")
	}
	return b.Commit.ID, nil
}

type Pipeline struct {
	ID     int64  `json:"id"`
	SHA    string `json:"sha"`
	Status string `json:"status"`
}
type MergeRequest struct {
	Description         string    `json:"description"`
	IID                 int64     `json:"iid"`
	URL                 string    `json:"web_url"`
	SourceBranch        string    `json:"source_branch"`
	TargetBranch        string    `json:"target_branch"`
	SHA                 string    `json:"sha"`
	State               string    `json:"state"`
	DetailedMergeStatus string    `json:"detailed_merge_status"`
	HasConflicts        bool      `json:"has_conflicts"`
	HeadPipeline        *Pipeline `json:"head_pipeline"`
}

func mrPath(repoID, iid int64) string {
	return fmt.Sprintf("%s/merge_requests/%d", project(repoID), iid)
}
func (c *Client) GetMR(ctx context.Context, repoID, iid int64) (MergeRequest, error) {
	var mr MergeRequest
	_, err := c.request(ctx, "GET", mrPath(repoID, iid), nil, &mr)
	return mr, err
}
func (c *Client) FindMR(ctx context.Context, repoID int64, source, target string) (*MergeRequest, error) {
	rows, err := paginate[MergeRequest](ctx, c, project(repoID)+"/merge_requests", url.Values{"state": {"opened"}, "source_branch": {source}, "target_branch": {target}})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.SourceBranch == source && r.TargetBranch == target && r.State == "opened" {
			return &r, nil
		}
	}
	return nil, nil
}
func (c *Client) RequiredCIPassed(ctx context.Context, repoID, mr int64, expectedSHA string, requireCI bool) (bool, error) {
	m, err := c.GetMR(ctx, repoID, mr)
	if err != nil {
		return false, err
	}
	if expectedSHA == "" || m.SHA != expectedSHA {
		return false, nil
	}
	if !requireCI {
		return true, nil
	}
	return ciPassed(m, expectedSHA), nil
}

// ClosingIssues uses GitLab's parsed commit/MR references, including instance-specific closing patterns.
func (c *Client) ClosingIssues(ctx context.Context, repoID, mrID int64) ([]domain.IssueRef, error) {
	rows, e := paginate[apiIssue](ctx, c, mrPath(repoID, mrID)+"/closes_issues", nil)
	if e != nil {
		return nil, e
	}
	out := []domain.IssueRef{}
	for _, r := range rows {
		out = append(out, domain.IssueRef{ProjectID: r.ProjectID, IID: r.IID})
	}
	return out, nil
}
func ciPassed(m MergeRequest, sha string) bool {
	return m.HeadPipeline != nil && m.HeadPipeline.SHA == sha && m.HeadPipeline.Status == "success"
}
func (c *Client) UpdateLabels(ctx context.Context, issue domain.Issue, add, remove []string) error {
	body := map[string]string{}
	if len(add) > 0 {
		body["add_labels"] = strings.Join(add, ",")
	}
	if len(remove) > 0 {
		body["remove_labels"] = strings.Join(remove, ",")
	}
	if len(body) == 0 {
		return nil
	}
	_, err := c.request(ctx, "PUT", issuePath(issue), body, nil)
	return err
}
func (c *Client) UpsertProgress(ctx context.Context, issue domain.Issue, taskID, body string) error {
	c.notes.Lock()
	defer c.notes.Unlock()
	marker := fmt.Sprintf("<!-- swarmjet-progress:%x -->", sha256.Sum256([]byte(taskID)))
	body = marker + "\n" + body
	notes, err := paginate[struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}](ctx, c, issuePath(issue)+"/notes", nil)
	if err != nil {
		return err
	}
	for _, n := range notes {
		if strings.HasPrefix(n.Body, marker+"\n") {
			if n.Body == body {
				return nil
			}
			_, err = c.request(ctx, "PUT", fmt.Sprintf("%s/notes/%d", issuePath(issue), n.ID), map[string]string{"body": body}, nil)
			return err
		}
	}
	_, err = c.request(ctx, "POST", issuePath(issue)+"/notes", map[string]string{"body": body}, nil)
	return err
}
func (c *Client) CloseIssue(ctx context.Context, issue domain.Issue) error {
	i, err := c.GetIssue(ctx, issue.ProjectID, issue.IID)
	if err != nil {
		return err
	}
	if i.State == "closed" {
		return nil
	}
	_, err = c.request(ctx, "PUT", issuePath(issue), map[string]string{"state_event": "close"}, nil)
	return err
}

// Merge is an optional helper. It conservatively requires successful CI for this exact SHA.
// GitLab atomically checks the submitted source SHA. Callers must also serialize target changes.
func (c *Client) Merge(ctx context.Context, repoID, mr int64, sha, target string) error {
	m, err := c.GetMR(ctx, repoID, mr)
	if err != nil {
		return err
	}
	if sha == "" || target == "" || m.SHA != sha || m.TargetBranch != target {
		return errors.New("merge request revision or target changed")
	}
	if m.State == "merged" {
		return nil
	}
	if m.State != "opened" || m.HasConflicts || m.DetailedMergeStatus != "mergeable" || !ciPassed(m, sha) {
		return errors.New("merge request is not ready for exact-SHA merge")
	}
	_, err = c.request(ctx, "PUT", mrPath(repoID, mr)+"/merge", map[string]any{"sha": sha, "auto_merge": false, "should_remove_source_branch": false}, nil)
	return err
}

func (c *Client) UpdateMRDescription(ctx context.Context, repoID, iid int64, description string) error {
	_, err := c.request(ctx, "PUT", mrPath(repoID, iid), map[string]string{"description": description}, nil)
	return err
}
