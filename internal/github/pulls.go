package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
)

type apiBranch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Commit    struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

func (b apiBranch) domain() forge.Branch {
	out := forge.Branch{Name: b.Name, Protected: b.Protected}
	out.Commit.ID = b.Commit.SHA
	return out
}
func (c *Client) GetBranch(ctx context.Context, id int64, name string) (forge.Branch, error) {
	p, e := c.repo(ctx, id)
	if e != nil {
		return forge.Branch{}, e
	}
	var b apiBranch
	_, e = c.request(ctx, "GET", p+"/branches/"+url.PathEscape(name), nil, &b)
	return b.domain(), e
}
func (c *Client) ListBranches(ctx context.Context, id int64) ([]forge.Branch, error) {
	p, e := c.repo(ctx, id)
	if e != nil {
		return nil, e
	}
	rows, e := paginate[apiBranch](ctx, c, p+"/branches", nil)
	out := make([]forge.Branch, 0, len(rows))
	for _, b := range rows {
		out = append(out, b.domain())
	}
	return out, e
}
func (c *Client) EnsureTarget(ctx context.Context, r domain.Repository) (string, error) {
	if r.TargetBranch == "" {
		return "", errors.New("target branch is required")
	}
	b, e := c.GetBranch(ctx, r.ProjectID, r.TargetBranch)
	if e == nil {
		if b.Commit.ID == "" {
			return "", errors.New("branch has no SHA")
		}
		return b.Commit.ID, nil
	}
	if code, ok := StatusCode(e); !ok || code != 404 {
		return "", e
	}
	if r.CreateFrom == "" {
		return "", errors.New("missing target requires explicit createFrom")
	}
	p, e := c.repo(ctx, r.ProjectID)
	if e != nil {
		return "", e
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if _, e = c.request(ctx, "GET", p+"/commits/"+url.PathEscape(r.CreateFrom), nil, &commit); e != nil {
		return "", e
	}
	if commit.SHA == "" {
		return "", errors.New("createFrom resolved to empty SHA")
	}
	_, e = c.request(ctx, "POST", p+"/git/refs", map[string]string{"ref": "refs/heads/" + r.TargetBranch, "sha": commit.SHA}, nil)
	if code, ok := StatusCode(e); e != nil && (!ok || code != 422) {
		return "", e
	}
	b, e = c.GetBranch(ctx, r.ProjectID, r.TargetBranch)
	if e != nil {
		return "", e
	}
	if b.Commit.ID != commit.SHA {
		return "", errors.New("created target branch does not match explicit base")
	}
	return b.Commit.ID, nil
}

type apiPR struct {
	Number         int64  `json:"number"`
	NodeID         string `json:"node_id"`
	Body           string `json:"body"`
	URL            string `json:"html_url"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	Draft          bool   `json:"draft"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	Head           struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo struct {
			ID int64 `json:"id"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
}

func (p apiPR) domain() forge.MergeRequest {
	state := p.State
	if p.Merged {
		state = "merged"
	} else if state == "open" {
		state = "opened"
	}
	status := p.MergeableState
	if p.Mergeable != nil && *p.Mergeable && !p.Draft && (status == "clean" || status == "unstable" || status == "has_hooks") {
		status = "mergeable"
	}
	return forge.MergeRequest{IID: p.Number, Description: p.Body, URL: p.URL, SourceBranch: p.Head.Ref, TargetBranch: p.Base.Ref, SHA: p.Head.SHA, State: state, DetailedMergeStatus: status, HasConflicts: p.Mergeable != nil && !*p.Mergeable}
}
func (c *Client) pull(ctx context.Context, id, number int64) (apiPR, error) {
	p, e := c.repo(ctx, id)
	if e != nil {
		return apiPR{}, e
	}
	var out apiPR
	_, e = c.request(ctx, "GET", fmt.Sprintf("%s/pulls/%d", p, number), nil, &out)
	return out, e
}
func (c *Client) GetMR(ctx context.Context, id, number int64) (forge.MergeRequest, error) {
	p, e := c.pull(ctx, id, number)
	return p.domain(), e
}
func (c *Client) FindMR(ctx context.Context, id int64, source, target string) (*forge.MergeRequest, error) {
	p, e := c.repo(ctx, id)
	if e != nil {
		return nil, e
	}
	owner := strings.Split(strings.TrimPrefix(p, "/repos/"), "/")[0]
	rows, e := paginate[apiPR](ctx, c, p+"/pulls", url.Values{"state": {"open"}, "head": {owner + ":" + source}, "base": {target}})
	if e != nil {
		return nil, e
	}
	for _, r := range rows {
		if r.Head.Ref == source && r.Base.Ref == target && r.Head.Repo.ID == id {
			m := r.domain()
			return &m, nil
		}
	}
	return nil, nil
}
func (c *Client) CreateOrUpdatePR(ctx context.Context, id int64, source, target, title, body string) (forge.MergeRequest, error) {
	existing, e := c.FindMR(ctx, id, source, target)
	if e != nil {
		return forge.MergeRequest{}, e
	}
	p, e := c.repo(ctx, id)
	if e != nil {
		return forge.MergeRequest{}, e
	}
	method, path := "POST", p+"/pulls"
	data := map[string]string{"head": source, "base": target, "title": title, "body": body}
	if existing != nil {
		method = "PATCH"
		path = fmt.Sprintf("%s/pulls/%d", p, existing.IID)
		delete(data, "head")
	}
	var out apiPR
	_, e = c.request(ctx, method, path, data, &out)
	if code, ok := StatusCode(e); ok && code == 422 && existing == nil {
		found, findErr := c.FindMR(ctx, id, source, target)
		if findErr == nil && found != nil {
			return *found, nil
		}
	}
	return out.domain(), e
}
func (c *Client) UpdateMRDescription(ctx context.Context, id, number int64, description string) error {
	p, e := c.repo(ctx, id)
	if e != nil {
		return e
	}
	_, e = c.request(ctx, "PATCH", fmt.Sprintf("%s/pulls/%d", p, number), map[string]string{"body": description}, nil)
	return e
}
func (c *Client) ClosingIssues(ctx context.Context, id, number int64) ([]domain.IssueRef, error) {
	r, e := c.GetRepository(ctx, id)
	if e != nil {
		return nil, e
	}
	parts := strings.Split(r.FullName, "/")
	var out []domain.IssueRef
	cursor := ""
	for page := 0; page < 10000; page++ {
		var data struct {
			Repository struct {
				PullRequest struct {
					Number  int64 `json:"number"`
					Closing struct {
						Nodes []struct {
							Number     int64 `json:"number"`
							Repository struct {
								DatabaseID int64 `json:"databaseId"`
							} `json:"repository"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"closingIssuesReferences"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		var after any
		if cursor != "" {
			after = cursor
		}
		e = c.graphql(ctx, `query($owner:String!,$repo:String!,$number:Int!,$after:String){repository(owner:$owner,name:$repo){pullRequest(number:$number){number closingIssuesReferences(first:100,after:$after){nodes{number repository{databaseId}} pageInfo{hasNextPage endCursor}}}}}`, map[string]any{"owner": parts[0], "repo": parts[1], "number": number, "after": after}, &data)
		if e != nil {
			return nil, e
		}
		if data.Repository.PullRequest.Number != number {
			return nil, errors.New("GitHub returned no pull request closing references")
		}
		conn := data.Repository.PullRequest.Closing
		for _, n := range conn.Nodes {
			out = append(out, domain.IssueRef{Provider: domain.ProviderGitHub, ProjectID: n.Repository.DatabaseID, IID: n.Number})
		}
		if !conn.PageInfo.HasNextPage {
			commits, err := c.commitClosingIssues(ctx, id, number)
			if err != nil {
				return nil, err
			}
			return append(out, commits...), nil
		}
		if conn.PageInfo.EndCursor == "" || conn.PageInfo.EndCursor == cursor {
			return nil, errors.New("invalid GitHub closing issue pagination")
		}
		cursor = conn.PageInfo.EndCursor
	}
	return nil, errors.New("GitHub closing issue pagination exceeds limit")
}

var _ forge.Client = (*Client)(nil)

// Commit closing keywords can close issues without appearing among the PR's
// linked issues, so inspect every commit in addition to GitHub's parsed links.
var closingKeyword = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+(?:https://github\.com/([a-z0-9_.-]+/[a-z0-9_.-]+)/issues/|([a-z0-9_.-]+/[a-z0-9_.-]+)?#)([0-9]+)`)

func (c *Client) commitClosingIssues(ctx context.Context, id, number int64) ([]domain.IssueRef, error) {
	p, err := c.repo(ctx, id)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(p, "/repos/"), "/")
	var messages []string
	cursor := ""
	for page := 0; page < 10000; page++ {
		var data struct {
			Repository struct {
				PullRequest struct {
					Number  int64 `json:"number"`
					Commits struct {
						Nodes []struct {
							Commit struct {
								Message string `json:"message"`
							} `json:"commit"`
						} `json:"nodes"`
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"commits"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		var after any
		if cursor != "" {
			after = cursor
		}
		err = c.graphql(ctx, `query($owner:String!,$repo:String!,$number:Int!,$after:String){repository(owner:$owner,name:$repo){pullRequest(number:$number){number commits(first:100,after:$after){nodes{commit{message}} pageInfo{hasNextPage endCursor}}}}}`, map[string]any{"owner": parts[0], "repo": parts[1], "number": number, "after": after}, &data)
		if err != nil {
			return nil, err
		}
		if data.Repository.PullRequest.Number != number {
			return nil, errors.New("GitHub returned no pull request commits")
		}
		conn := data.Repository.PullRequest.Commits
		for _, node := range conn.Nodes {
			messages = append(messages, node.Commit.Message)
		}
		if !conn.PageInfo.HasNextPage {
			break
		}
		if conn.PageInfo.EndCursor == "" || conn.PageInfo.EndCursor == cursor || page == 9999 {
			return nil, errors.New("invalid GitHub commit pagination")
		}
		cursor = conn.PageInfo.EndCursor
	}
	var out []domain.IssueRef
	for _, message := range messages {
		for _, match := range closingKeyword.FindAllStringSubmatch(message, -1) {
			repositoryID := id
			name := match[1]
			if name == "" {
				name = match[2]
			}
			if name != "" {
				var repository Repository
				if _, err = c.request(ctx, "GET", "/repos/"+name, nil, &repository); err != nil {
					return nil, err
				}
				if repository.ID <= 0 {
					return nil, errors.New("invalid closing issue repository")
				}
				repositoryID = repository.ID
			}
			issueNumber, err := strconv.ParseInt(match[3], 10, 64)
			if err != nil {
				return nil, err
			}
			out = append(out, domain.IssueRef{Provider: domain.ProviderGitHub, ProjectID: repositoryID, IID: issueNumber})
		}
	}
	return out, nil
}
