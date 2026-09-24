package gitlab

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
)

// RepositoryFile reads one file at a pinned ref through the GitLab API.
func (c *Client) RepositoryFile(ctx context.Context, projectID int64, name, ref string) ([]byte, error) {
	var file struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	path := project(projectID) + "/repository/files/" + url.PathEscape(name) + "?ref=" + url.QueryEscape(ref)
	if _, err := c.request(ctx, "GET", path, nil, &file); err != nil {
		return nil, err
	}
	if file.Encoding != "base64" || len(file.Content) > 2<<20 {
		return nil, errors.New("unsupported or oversized Meta file")
	}
	return base64.StdEncoding.DecodeString(file.Content)
}

func (c *Client) CreateFileCommit(ctx context.Context, projectID int64, branch, baseSHA, name, content, message string) (string, error) {
	var result struct {
		ID string `json:"id"`
	}
	_, err := c.request(ctx, "POST", project(projectID)+"/repository/commits", map[string]any{
		"branch": branch, "start_sha": baseSHA, "commit_message": message,
		"actions": []map[string]string{{"action": "update", "file_path": name, "content": content}},
	}, &result)
	if err != nil {
		return "", err
	}
	if result.ID == "" {
		return "", errors.New("GitLab returned an empty commit ID")
	}
	return result.ID, nil
}

func (c *Client) CreateMR(ctx context.Context, projectID int64, source, target, title, description string) (MergeRequest, error) {
	var mr MergeRequest
	_, err := c.request(ctx, "POST", project(projectID)+"/merge_requests", map[string]any{
		"source_branch": source, "target_branch": target, "title": title,
		"description": description, "remove_source_branch": true,
	}, &mr)
	return mr, err
}

func (c *Client) MetaProjectByID(ctx context.Context, id int64) (MetaProject, error) {
	return c.MetaProject(ctx, strconv.FormatInt(id, 10))
}

func (c *Client) CommitSHA(ctx context.Context, projectID int64, ref string) (string, error) {
	var commit struct {
		ID string `json:"id"`
	}
	_, err := c.request(ctx, "GET", project(projectID)+"/repository/commits/"+url.PathEscape(ref), nil, &commit)
	if err != nil {
		return "", err
	}
	if commit.ID == "" {
		return "", errors.New("GitLab returned an empty commit ID")
	}
	return commit.ID, nil
}
