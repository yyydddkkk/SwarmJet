package github

import (
	"context"
	"fmt"
	"net/url"
	"sync"
)

// ReadListedChecks collects independent read-only inputs concurrently for the
// console. RequiredChecks remains the merge gate's live, sequential path.
func (c *Client) ReadListedChecks(ctx context.Context, id int64, branch, sha string) (CheckResult, []CheckRun, error) {
	if branch == "" || sha == "" {
		return CheckResult{}, nil, fmt.Errorf("listed pull request lacks base branch or head")
	}
	p, err := c.repo(ctx, id)
	if err != nil {
		return CheckResult{}, nil, err
	}

	var protection struct {
		Checks   []requiredCheck `json:"checks"`
		Contexts []string        `json:"contexts"`
	}
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			Checks []requiredCheck `json:"required_status_checks"`
		} `json:"parameters"`
	}
	var statuses []requiredStatus
	var runs []CheckRun
	var errs [4]error
	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		_, errs[0] = c.request(ctx, "GET", p+"/branches/"+url.PathEscape(branch)+"/protection/required_status_checks", nil, &protection)
	}()
	go func() {
		defer wg.Done()
		rules, errs[1] = paginate[struct {
			Type       string `json:"type"`
			Parameters struct {
				Checks []requiredCheck `json:"required_status_checks"`
			} `json:"parameters"`
		}](ctx, c, p+"/rules/branches/"+url.PathEscape(branch), nil)
	}()
	go func() {
		defer wg.Done()
		statuses, errs[2] = paginate[requiredStatus](ctx, c, p+"/commits/"+url.PathEscape(sha)+"/statuses", nil)
	}()
	go func() { defer wg.Done(); runs, errs[3] = c.CheckRuns(ctx, id, sha) }()
	wg.Wait()
	if code, ok := StatusCode(errs[0]); ok && (code == 403 || code == 404) {
		errs[0] = nil
	}
	for _, e := range errs {
		if e != nil {
			return CheckResult{}, nil, e
		}
	}
	required := append([]requiredCheck{}, protection.Checks...)
	for _, name := range protection.Contexts {
		found := false
		for _, check := range required {
			if check.Context == name {
				found = true
				break
			}
		}
		if !found {
			required = append(required, requiredCheck{Context: name})
		}
	}
	queue := false
	for _, rule := range rules {
		if rule.Type == "merge_queue" {
			queue = true
		}
		if rule.Type == "required_status_checks" {
			required = append(required, rule.Parameters.Checks...)
		}
	}
	return evaluateRequiredChecks(required, queue, statuses, runs, sha), runs, nil
}
