package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
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

// RepositoryID resolves an "owner/name" repository to the numeric ID GitHub
// uses in the rest of the API. The result is cached for repo().
func (c *Client) RepositoryID(ctx context.Context, fullName string) (int64, error) {
	fullName = strings.TrimSpace(fullName)
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, fmt.Errorf("invalid repository %q", fullName)
	}
	var repo struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	path := "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
	if _, err := c.request(ctx, "GET", path, nil, &repo); err != nil {
		return 0, err
	}
	if repo.ID <= 0 || !strings.EqualFold(repo.FullName, fullName) {
		return 0, errors.New("GitHub returned an unexpected repository")
	}
	c.names.Store(repo.ID, repo.FullName)
	return repo.ID, nil
}

// PullRequest is the live state of one PR. Mergeability is a pointer because
// GitHub computes it asynchronously and reports null until it is known.
type PullRequest struct {
	Number         int64  `json:"number"`
	State          string `json:"state"`
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	URL            string `json:"url"`
	HeadSHA        string `json:"headSha"`
	HeadRef        string `json:"headRef"`
	BaseRef        string `json:"baseRef"`
	BaseRepository string `json:"baseRepository"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeableState"`
}

// GetPullRequest reads the fields the merge gate needs, including the base
// repository and the mergeable state that the forge view does not expose.
func (c *Client) GetPullRequest(ctx context.Context, id, number int64) (PullRequest, error) {
	p, err := c.repo(ctx, id)
	if err != nil {
		return PullRequest{}, err
	}
	var raw struct {
		Number         int64  `json:"number"`
		State          string `json:"state"`
		Draft          bool   `json:"draft"`
		Merged         bool   `json:"merged"`
		HTMLURL        string `json:"html_url"`
		Mergeable      *bool  `json:"mergeable"`
		MergeableState string `json:"mergeable_state"`
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
		return PullRequest{}, err
	}
	return PullRequest{
		Number:         raw.Number,
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
// external text exactly like a PR comment or CI log.
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

// CheckRuns lists the latest check runs for one commit, newest app run per
// name, and follows GitHub's pagination.
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
		if page == 10000 {
			return nil, errors.New("GitHub checks pagination exceeds limit")
		}
	}
	return all, nil
}
