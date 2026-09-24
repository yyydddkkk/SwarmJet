package github

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

type CheckResult struct {
	Ready         bool   `json:"ready"`
	Waiting       bool   `json:"waiting"`
	Failed        bool   `json:"failed"`
	Reason        string `json:"reason"`
	RequiredCount int    `json:"requiredCount"`
	MergeQueue    bool   `json:"mergeQueue"`
}
type requiredCheck struct {
	Context       string `json:"context"`
	AppID         *int64 `json:"app_id"`
	IntegrationID *int64 `json:"integration_id"`
}

func (c *Client) requirements(ctx context.Context, id int64, branch string) ([]requiredCheck, bool, error) {
	p, e := c.repo(ctx, id)
	if e != nil {
		return nil, false, e
	}
	var protection struct {
		Checks   []requiredCheck `json:"checks"`
		Contexts []string        `json:"contexts"`
	}
	_, e = c.request(ctx, "GET", p+"/branches/"+url.PathEscape(branch)+"/protection/required_status_checks", nil, &protection)
	if code, ok := StatusCode(e); e != nil && (!ok || code != 404) {
		return nil, false, e
	}
	checks := protection.Checks
	for _, name := range protection.Contexts {
		found := false
		for _, r := range checks {
			if r.Context == name {
				found = true
			}
		}
		if !found {
			checks = append(checks, requiredCheck{Context: name})
		}
	}
	rules, e := paginate[struct {
		Type       string `json:"type"`
		Parameters struct {
			Checks []requiredCheck `json:"required_status_checks"`
		} `json:"parameters"`
	}](ctx, c, p+"/rules/branches/"+url.PathEscape(branch), nil)
	if e != nil {
		return nil, false, e
	}
	queue := false
	for _, r := range rules {
		if r.Type == "merge_queue" {
			queue = true
		}
		if r.Type == "required_status_checks" {
			checks = append(checks, r.Parameters.Checks...)
		}
	}
	return checks, queue, nil
}
func (c *Client) RequiredChecks(ctx context.Context, id, number int64, sha string) (CheckResult, error) {
	pr, e := c.pull(ctx, id, number)
	if e != nil {
		return CheckResult{}, e
	}
	if sha == "" || pr.Head.SHA != sha {
		return CheckResult{Failed: true, Reason: "pull request head changed"}, nil
	}
	required, queue, e := c.requirements(ctx, id, pr.Base.Ref)
	if e != nil {
		return CheckResult{}, e
	}
	result := CheckResult{RequiredCount: len(required), MergeQueue: queue}
	if len(required) == 0 {
		result.Failed = true
		result.Reason = "target branch has no required checks"
		return result, nil
	}
	p, e := c.repo(ctx, id)
	if e != nil {
		return result, e
	}
	statuses, e := paginate[struct {
		Context string `json:"context"`
		State   string `json:"state"`
	}](ctx, c, p+"/commits/"+url.PathEscape(sha)+"/statuses", nil)
	if e != nil {
		return result, e
	}
	latestStatus := map[string]string{}
	for _, s := range statuses {
		if _, ok := latestStatus[s.Context]; !ok {
			latestStatus[s.Context] = s.State
		}
	}
	type check struct {
		ID         int64  `json:"id"`
		Name       string `json:"name"`
		HeadSHA    string `json:"head_sha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		App        struct {
			ID int64 `json:"id"`
		} `json:"app"`
	}
	var checks []check
	for page := 1; page <= 10000; page++ {
		var out struct {
			Runs []check `json:"check_runs"`
		}
		h, e := c.request(ctx, "GET", fmt.Sprintf("%s/commits/%s/check-runs?filter=latest&per_page=100&page=%d", p, url.PathEscape(sha), page), nil, &out)
		if e != nil {
			return result, e
		}
		checks = append(checks, out.Runs...)
		if !strings.Contains(h.Get("Link"), `rel="next"`) {
			break
		}
		if page == 10000 {
			return result, errors.New("GitHub checks pagination exceeds limit")
		}
	}
	for _, r := range required {
		app := r.AppID
		if r.IntegrationID != nil {
			app = r.IntegrationID
		}
		matched := false
		passed := true
		failed := false
		if state, ok := latestStatus[r.Context]; ok && (app == nil || *app <= 0) {
			matched = true
			passed = state == "success"
			failed = state == "error" || state == "failure"
		}
		latest := map[int64]check{}
		for _, ch := range checks {
			if ch.Name != r.Context || ch.HeadSHA != sha || (app != nil && *app > 0 && ch.App.ID != *app) {
				continue
			}
			if prev, ok := latest[ch.App.ID]; !ok || ch.ID > prev.ID {
				latest[ch.App.ID] = ch
			}
		}
		for _, ch := range latest {
			matched = true
			ok := ch.Status == "completed" && (ch.Conclusion == "success" || ch.Conclusion == "neutral" || ch.Conclusion == "skipped")
			passed = passed && ok
			if ch.Status == "completed" && !ok {
				failed = true
			}
		}
		if !matched || !passed {
			if failed {
				result.Failed = true
				result.Reason = "required check failed: " + r.Context
			} else {
				result.Waiting = true
				if result.Reason == "" {
					result.Reason = "required check not passed: " + r.Context
				}
			}
		}
	}
	if result.Failed {
		result.Waiting = false
	}
	result.Ready = !result.Failed && !result.Waiting
	return result, nil
}
func (c *Client) RequiredCIPassed(ctx context.Context, id, number int64, sha string, require bool) (bool, error) {
	if !require {
		p, e := c.pull(ctx, id, number)
		return sha != "" && p.Head.SHA == sha, e
	}
	result, e := c.RequiredChecks(ctx, id, number, sha)
	return result.Ready, e
}

type MergeResult struct {
	Merged       bool   `json:"merged"`
	Queued       bool   `json:"queued"`
	QueueEntryID string `json:"queueEntryId,omitempty"`
}

func (c *Client) RequestMerge(ctx context.Context, id, number int64, sha, method string) (MergeResult, error) {
	var result MergeResult
	if method == "" {
		method = "merge"
	}
	if method != "merge" && method != "squash" && method != "rebase" {
		return result, errors.New("invalid merge method")
	}
	pr, e := c.pull(ctx, id, number)
	if e != nil {
		return result, e
	}
	if sha == "" || pr.Head.SHA != sha {
		return result, errors.New("pull request head changed")
	}
	if pr.Merged {
		return MergeResult{Merged: true}, nil
	}
	if pr.State != "open" || pr.Draft {
		return result, errors.New("pull request is not open and ready")
	}
	repo, e := c.GetRepository(ctx, id)
	if e != nil {
		return result, e
	}
	if (method == "merge" && !repo.AllowMergeCommit) || (method == "squash" && !repo.AllowSquashMerge) || (method == "rebase" && !repo.AllowRebaseMerge) {
		return result, errors.New("repository disallows selected merge method")
	}
	_, queue, e := c.requirements(ctx, id, pr.Base.Ref)
	if e != nil {
		return result, e
	}
	if queue {
		var state struct {
			Node struct {
				Entry *struct {
					ID string `json:"id"`
				} `json:"mergeQueueEntry"`
			} `json:"node"`
		}
		e = c.graphql(ctx, `query($id:ID!){node(id:$id){... on PullRequest{mergeQueueEntry{id}}}}`, map[string]any{"id": pr.NodeID}, &state)
		if e != nil {
			return result, e
		}
		if state.Node.Entry != nil {
			return MergeResult{Queued: true, QueueEntryID: state.Node.Entry.ID}, nil
		}
		var data struct {
			Enqueue struct {
				Entry struct {
					ID string `json:"id"`
				} `json:"mergeQueueEntry"`
			} `json:"enqueuePullRequest"`
		}
		e = c.graphql(ctx, `mutation($id:ID!,$sha:GitObjectID!){enqueuePullRequest(input:{pullRequestId:$id,expectedHeadOid:$sha}){mergeQueueEntry{id}}}`, map[string]any{"id": pr.NodeID, "sha": sha}, &data)
		if e != nil {
			return result, e
		}
		if data.Enqueue.Entry.ID == "" {
			return result, errors.New("GitHub returned no merge queue entry")
		}
		return MergeResult{Queued: true, QueueEntryID: data.Enqueue.Entry.ID}, nil
	}
	p, e := c.repo(ctx, id)
	if e != nil {
		return result, e
	}
	_, e = c.request(ctx, "PUT", fmt.Sprintf("%s/pulls/%d/merge", p, number), map[string]string{"sha": sha, "merge_method": method}, &result)
	if e == nil && !result.Merged {
		e = errors.New("GitHub did not merge pull request")
	}
	return result, e
}
func (c *Client) Merge(ctx context.Context, id, number int64, sha, target string) error {
	pr, e := c.pull(ctx, id, number)
	if e != nil {
		return e
	}
	if target == "" || pr.Base.Ref != target || pr.Head.SHA != sha {
		return errors.New("pull request revision or target changed")
	}
	checks, e := c.RequiredChecks(ctx, id, number, sha)
	if e != nil {
		return e
	}
	if !checks.Ready {
		return errors.New(checks.Reason)
	}
	_, e = c.RequestMerge(ctx, id, number, sha, c.mergeMethod)
	return e
}

// GetMergeState polls a previously submitted merge without re-enqueueing a removed PR.
func (c *Client) GetMergeState(ctx context.Context, id, number int64, expectedSHA string) (MergeResult, error) {
	pr, err := c.pull(ctx, id, number)
	if err != nil {
		return MergeResult{}, err
	}
	if expectedSHA == "" || pr.Head.SHA != expectedSHA {
		return MergeResult{}, errors.New("pull request head changed")
	}
	if pr.Merged {
		return MergeResult{Merged: true}, nil
	}
	var data struct {
		Node struct {
			Entry *struct {
				ID string `json:"id"`
			} `json:"mergeQueueEntry"`
		} `json:"node"`
	}
	if err = c.graphql(ctx, `query($id:ID!){node(id:$id){... on PullRequest{mergeQueueEntry{id}}}}`, map[string]any{"id": pr.NodeID}, &data); err != nil {
		return MergeResult{}, err
	}
	if data.Node.Entry == nil {
		return MergeResult{}, nil
	}
	return MergeResult{Queued: true, QueueEntryID: data.Node.Entry.ID}, nil
}

func (c *Client) CancelMergeQueue(ctx context.Context, id, number int64) error {
	pr, err := c.pull(ctx, id, number)
	if err != nil {
		return err
	}
	var out any
	return c.graphql(ctx, `mutation($id:ID!){dequeuePullRequest(input:{pullRequestId:$id}){clientMutationId}}`, map[string]any{"id": pr.NodeID}, &out)
}
