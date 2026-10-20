package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// NewWithBaseURL builds a client for a GitHub-compatible API endpoint. Tests
// use it with httptest; it also works for GitHub Enterprise-style deployments.
func NewWithBaseURL(source TokenSource, baseURL string) *Client {
	c := New(source)
	if baseURL != "" {
		c.baseURL = strings.TrimRight(baseURL, "/")
	}
	return c
}

// CreatePRComment posts a comment on a pull request as the authenticated App and
// returns its html_url. GitHub serves PR comments through the issues endpoint,
// which is also why the App can comment even though it is the PR author.
func (c *Client) CreatePRComment(ctx context.Context, id, number int64, body string) (string, error) {
	if number <= 0 {
		return "", errors.New("invalid pull request number")
	}
	if strings.TrimSpace(body) == "" {
		return "", errors.New("empty comment body")
	}
	p, err := c.repo(ctx, id)
	if err != nil {
		return "", err
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	if _, err = c.request(ctx, "POST", fmt.Sprintf("%s/issues/%d/comments", p, number), map[string]string{"body": body}, &out); err != nil {
		return "", err
	}
	if out.HTMLURL == "" {
		return "", errors.New("GitHub returned no comment URL")
	}
	return out.HTMLURL, nil
}

// PullState is the live pull-request state the merge gate needs. Mergeable is a
// pointer because GitHub computes it asynchronously and reports null until it
// is known.
type PullState struct {
	Title          string    `json:"title"`
	UpdatedAt      time.Time `json:"updatedAt"`
	ClosedAt       time.Time `json:"closedAt"`
	Number         int64     `json:"number"`
	State          string    `json:"state"`
	Draft          bool      `json:"draft"`
	Merged         bool      `json:"merged"`
	URL            string    `json:"url"`
	HeadSHA        string    `json:"headSha"`
	HeadRef        string    `json:"headRef"`
	BaseRef        string    `json:"baseRef"`
	BaseRepository string    `json:"baseRepository"`
	Mergeable      *bool     `json:"mergeable,omitempty"`
	MergeableState string    `json:"mergeableState,omitempty"`
}

// GetPullState reads the fields the merge gate needs, including the base
// repository and the mergeable state the forge view does not expose.
func (c *Client) GetPullState(ctx context.Context, id, number int64) (PullState, error) {
	p, err := c.repo(ctx, id)
	if err != nil {
		return PullState{}, err
	}
	var raw struct {
		Title          string     `json:"title"`
		UpdatedAt      time.Time  `json:"updated_at"`
		ClosedAt       *time.Time `json:"closed_at"`
		Number         int64      `json:"number"`
		State          string     `json:"state"`
		Draft          bool       `json:"draft"`
		Merged         bool       `json:"merged"`
		HTMLURL        string     `json:"html_url"`
		Mergeable      *bool      `json:"mergeable"`
		MergeableState string     `json:"mergeable_state"`
		Head           struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref  string `json:"ref"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"base"`
	}
	if _, err = c.request(ctx, "GET", fmt.Sprintf("%s/pulls/%d", p, number), nil, &raw); err != nil {
		return PullState{}, err
	}
	closedAt := time.Time{}
	if raw.ClosedAt != nil {
		closedAt = raw.ClosedAt.UTC()
	}
	return PullState{
		ClosedAt:       closedAt,
		Number:         raw.Number,
		Title:          raw.Title,
		UpdatedAt:      raw.UpdatedAt.UTC(),
		State:          raw.State,
		Draft:          raw.Draft,
		Merged:         raw.Merged,
		URL:            raw.HTMLURL,
		HeadSHA:        raw.Head.SHA,
		HeadRef:        raw.Head.Ref,
		BaseRef:        raw.Base.Ref,
		BaseRepository: raw.Base.Repo.FullName,
		Mergeable:      raw.Mergeable,
		MergeableState: raw.MergeableState,
	}, nil
}

// CheckRun is one GitHub check run plus its output summary. Output is untrusted
// external text exactly like a PR comment or a CI log; callers must mark it.
type CheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HeadSHA    string `json:"headSha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"detailsUrl,omitempty"`
	HTMLURL    string `json:"htmlUrl,omitempty"`
	Output     struct {
		Title   string `json:"title,omitempty"`
		Summary string `json:"summary,omitempty"`
		Text    string `json:"text,omitempty"`
	} `json:"output"`
	App struct {
		ID int64 `json:"id"`
	} `json:"app"`
}

// CheckRuns lists the latest check runs for one commit and follows GitHub's
// pagination.
func (c *Client) CheckRuns(ctx context.Context, id int64, sha string) ([]CheckRun, error) {
	if sha == "" {
		return nil, errors.New("commit SHA is required")
	}
	p, err := c.repo(ctx, id)
	if err != nil {
		return nil, err
	}
	var all []CheckRun
	for page := 1; page <= 10000; page++ {
		var out struct {
			Runs []CheckRun `json:"check_runs"`
		}
		h, err := c.request(ctx, "GET", fmt.Sprintf("%s/commits/%s/check-runs?filter=latest&per_page=100&page=%d", p, url.PathEscape(sha), page), nil, &out)
		if err != nil {
			return nil, err
		}
		all = append(all, out.Runs...)
		if !strings.Contains(h.Get("Link"), `rel="next"`) {
			return all, nil
		}
	}
	return nil, errors.New("GitHub checks pagination exceeds limit")
}
