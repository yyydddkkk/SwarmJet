package github

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
	"time"
)

type apiIssue struct {
	DependenciesSummary struct {
		BlockedBy *int `json:"blocked_by"`
	} `json:"issue_dependencies_summary"`
	Number        int64     `json:"number"`
	Title         string    `json:"title"`
	Body          string    `json:"body"`
	URL           string    `json:"html_url"`
	RepositoryURL string    `json:"repository_url"`
	State         string    `json:"state"`
	PullRequest   *struct{} `json:"pull_request"`
	Labels        []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (i apiIssue) domain(id int64) domain.Issue {
	labels := make([]string, 0, len(i.Labels))
	for _, l := range i.Labels {
		labels = append(labels, l.Name)
	}
	state := i.State
	if state == "open" {
		state = "opened"
	}
	return domain.Issue{Provider: domain.ProviderGitHub, ProjectID: id, IID: i.Number, Title: i.Title, Description: i.Body, URL: i.URL, State: state, Labels: labels}
}
func (c *Client) issuePath(ctx context.Context, i domain.Issue) (string, error) {
	p, e := c.repo(ctx, i.ProjectID)
	return fmt.Sprintf("%s/issues/%d", p, i.IID), e
}
func (c *Client) ListEligibleIssues(ctx context.Context, id int64) ([]domain.Issue, error) {
	p, e := c.repo(ctx, id)
	if e != nil {
		return nil, e
	}
	rows, e := paginate[apiIssue](ctx, c, p+"/issues", url.Values{"state": {"open"}, "labels": {"autonomous"}, "sort": {"created"}, "direction": {"asc"}})
	if e != nil {
		return nil, e
	}
	var out []domain.Issue
	for _, r := range rows {
		if r.PullRequest == nil {
			out = append(out, r.domain(id))
		}
	}
	return out, nil
}
func (c *Client) GetIssue(ctx context.Context, id, number int64) (domain.Issue, error) {
	p, e := c.issuePath(ctx, domain.Issue{ProjectID: id, IID: number})
	if e != nil {
		if dependencyScopeError(e) {
			reader, err := c.dependencyReader(id, "")
			if err != nil {
				return domain.Issue{}, err
			}
			return reader.GetIssue(ctx, id, number)
		}
		return domain.Issue{}, e
	}
	var row apiIssue
	_, e = c.request(ctx, "GET", p, nil, &row)
	if dependencyScopeError(e) && c.dependencyToken != nil {
		reader, err := c.dependencyReader(id, "")
		if err != nil {
			return domain.Issue{}, err
		}
		return reader.GetIssue(ctx, id, number)
	}
	if e == nil && row.PullRequest != nil {
		e = errors.New("GitHub resource is a pull request")
	}
	return row.domain(id), e
}
func (c *Client) Dependencies(ctx context.Context, i domain.Issue) ([]domain.IssueRef, error) {
	p, e := c.issuePath(ctx, i)
	if e != nil {
		return nil, e
	}
	rows, e := paginate[apiIssue](ctx, c, p+"/dependencies/blocked_by", nil)
	if e != nil {
		return nil, e
	}
	var source apiIssue
	if _, e = c.request(ctx, "GET", p, nil, &source); e != nil {
		return nil, e
	}
	if source.DependenciesSummary.BlockedBy != nil && *source.DependenciesSummary.BlockedBy != len(rows) {
		return nil, errors.New("GitHub dependency list is incomplete or changed; a dependency may be outside the project credential scope")
	}
	var out []domain.IssueRef
	for _, row := range rows {
		u, e := url.Parse(row.RepositoryURL)
		if e != nil || u.Scheme != "https" || u.Host != "api.github.com" || u.RawQuery != "" || u.Fragment != "" || len(strings.Split(strings.TrimPrefix(u.Path, "/repos/"), "/")) != 2 || !strings.HasPrefix(u.Path, "/repos/") {
			return nil, errors.New("invalid dependency repository URL")
		}
		var r Repository
		if _, e = c.request(ctx, "GET", u.Path, nil, &r); e != nil {
			if !dependencyScopeError(e) {
				return nil, e
			}
			reader, err := c.dependencyReader(0, strings.TrimPrefix(u.Path, "/repos/"))
			if err != nil {
				return nil, err
			}
			if _, err = reader.request(ctx, "GET", u.Path, nil, &r); err != nil {
				return nil, fmt.Errorf("GitHub dependency repository is inaccessible: %w", err)
			}
		}
		if r.ID <= 0 {
			return nil, errors.New("invalid GitHub dependency repository")
		}
		c.names.Store(r.ID, r.FullName)
		out = append(out, domain.IssueRef{Provider: domain.ProviderGitHub, ProjectID: r.ID, IID: row.Number})
	}
	return out, nil
}
func (c *Client) ListIssueNotes(ctx context.Context, i domain.Issue) ([]forge.IssueNote, error) {
	p, e := c.issuePath(ctx, i)
	if e != nil {
		return nil, e
	}
	rows, e := paginate[struct {
		ID        int64     `json:"id"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
		UpdatedAt time.Time `json:"updated_at"`
		User      struct {
			ID int64 `json:"id"`
		} `json:"user"`
	}](ctx, c, p+"/comments", nil)
	if e != nil {
		return nil, e
	}
	out := make([]forge.IssueNote, 0, len(rows))
	for _, r := range rows {
		n := forge.IssueNote{ID: r.ID, Body: r.Body, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
		n.Author.ID = r.User.ID
		out = append(out, n)
	}
	return out, nil
}
func (c *Client) UpdateLabels(ctx context.Context, i domain.Issue, add, remove []string) error {
	p, e := c.issuePath(ctx, i)
	if e != nil {
		return e
	}
	if len(add) > 0 {
		if _, e = c.request(ctx, "POST", p+"/labels", map[string]any{"labels": add}, nil); e != nil {
			return e
		}
	}
	for _, label := range remove {
		_, e = c.request(ctx, "DELETE", p+"/labels/"+url.PathEscape(label), nil, nil)
		if code, ok := StatusCode(e); e != nil && (!ok || code != 404) {
			return e
		}
	}
	return nil
}
func (c *Client) ListProjectLabels(ctx context.Context, id int64) ([]forge.ProjectLabel, error) {
	p, e := c.repo(ctx, id)
	if e != nil {
		return nil, e
	}
	return paginate[forge.ProjectLabel](ctx, c, p+"/labels", nil)
}
func (c *Client) CreateProjectLabel(ctx context.Context, id int64, l forge.ProjectLabel) error {
	p, e := c.repo(ctx, id)
	if e != nil {
		return e
	}
	l.Color = strings.TrimPrefix(l.Color, "#")
	_, e = c.request(ctx, "POST", p+"/labels", l, nil)
	return e
}
func (c *Client) writeNote(ctx context.Context, i domain.Issue, id int64, body string) (int64, error) {
	p, e := c.repo(ctx, i.ProjectID)
	if e != nil {
		return 0, e
	}
	method := "PATCH"
	path := fmt.Sprintf("%s/issues/comments/%d", p, id)
	if id == 0 {
		method = "POST"
		path = fmt.Sprintf("%s/issues/%d/comments", p, i.IID)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	_, e = c.request(ctx, method, path, map[string]string{"body": body}, &out)
	if e == nil && out.ID == 0 {
		e = errors.New("GitHub returned no comment ID")
	}
	return out.ID, e
}
func (c *Client) UpsertProgress(ctx context.Context, i domain.Issue, task, body string) error {
	c.notes.Lock()
	defer c.notes.Unlock()
	marker := fmt.Sprintf("<!-- swarmjet-progress:%x -->", sha256.Sum256([]byte(task)))
	notes, e := c.ListIssueNotes(ctx, i)
	if e != nil {
		return e
	}
	body = marker + "\n" + body
	var id int64
	for _, n := range notes {
		if strings.HasPrefix(n.Body, marker+"\n") {
			if n.Body == body {
				return nil
			}
			id = n.ID
			break
		}
	}
	_, e = c.writeNote(ctx, i, id, body)
	return e
}
func (c *Client) UpsertIssueReview(ctx context.Context, i domain.Issue, task, digest, body string) (string, error) {
	c.notes.Lock()
	defer c.notes.Unlock()
	prefix := "<!-- swarmjet-review:" + task + ":"
	marker := prefix + digest + " -->"
	body = marker + "\n" + body
	notes, e := c.ListIssueNotes(ctx, i)
	if e != nil {
		return "", e
	}
	var id int64
	for _, n := range notes {
		if !strings.HasPrefix(n.Body, prefix) {
			continue
		}
		if strings.HasPrefix(n.Body, marker+"\n") {
			id = n.ID
			if n.Body != body {
				if _, e = c.writeNote(ctx, i, id, body); e != nil {
					return "", e
				}
			}
		} else if !strings.Contains(n.Body, "此版本已失效") {
			if _, e = c.writeNote(ctx, i, n.ID, n.Body+"\n\n> 此版本已失效；请查看该 Issue 中最新的 SwarmJet 复核指引。"); e != nil {
				return "", e
			}
		}
	}
	if id == 0 {
		id, e = c.writeNote(ctx, i, 0, body)
	}
	return strings.Split(i.URL, "#")[0] + fmt.Sprintf("#issuecomment-%d", id), e
}
func (c *Client) CloseIssue(ctx context.Context, i domain.Issue) error {
	p, e := c.issuePath(ctx, i)
	if e != nil {
		return e
	}
	_, e = c.request(ctx, "PATCH", p, map[string]string{"state": "closed"}, nil)
	return e
}
