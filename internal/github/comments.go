package github

import (
	"context"
	"errors"
	"fmt"
)

// CreatePRComment posts a comment on a pull request as the authenticated App and
// returns its html_url. GitHub serves PR comments through the issues endpoint,
// which is also why the App can comment even though it is the PR author.
func (c *Client) CreatePRComment(ctx context.Context, id, number int64, body string) (string, error) {
	if number <= 0 {
		return "", errors.New("invalid pull request number")
	}
	if body == "" {
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
