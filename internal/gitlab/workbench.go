package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/observability"
)

// Workbench records intentionally mirror GitLab's live response. No issue or MR
// body is persisted locally.
type WorkbenchUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}
type WorkbenchLabelOption struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}
type WorkbenchAssigneeOption struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatarUrl"`
}

// WorkbenchOptions reads labels and effective project members with the same
// personal identity used by workbench writes. GitLab's members/all endpoint
// includes inherited and invited group members.
func (c *Client) WorkbenchOptions(ctx context.Context, projectID int64, search string) ([]WorkbenchLabelOption, []WorkbenchAssigneeOption, bool, bool, error) {
	labels, labelsTruncated, err := workbenchOptionPages[WorkbenchLabelOption](ctx, c, fmt.Sprintf("/projects/%d/labels", projectID), nil, 10)
	if err != nil {
		return nil, nil, false, false, err
	}
	query := url.Values{}
	query.Set("state", "active")
	if search != "" {
		query.Set("query", search)
	}
	type member struct {
		ID        int64  `json:"id"`
		Name      string `json:"name"`
		Username  string `json:"username"`
		AvatarURL string `json:"avatar_url"`
		State     string `json:"state"`
	}
	members, membersTruncated, err := workbenchOptionPages[member](ctx, c, fmt.Sprintf("/projects/%d/members/all", projectID), query, 1)
	if err != nil {
		return nil, nil, false, false, err
	}
	assignees := make([]WorkbenchAssigneeOption, 0, len(members))
	for _, member := range members {
		if member.State != "" && member.State != "active" {
			continue
		}
		assignees = append(assignees, WorkbenchAssigneeOption{ID: member.ID, Name: member.Name, Username: member.Username, AvatarURL: member.AvatarURL})
	}
	if labels == nil {
		labels = []WorkbenchLabelOption{}
	}
	return labels, assignees, labelsTruncated, membersTruncated, nil
}

func workbenchOptionPages[T any](ctx context.Context, c *Client, path string, query url.Values, maxPages int) ([]T, bool, error) {
	if query == nil {
		query = url.Values{}
	}
	query.Set("per_page", "100")
	rows := []T{}
	page := 1
	for requestNumber := 1; requestNumber <= maxPages; requestNumber++ {
		query.Set("page", strconv.Itoa(page))
		var batch []T
		header, err := c.request(ctx, http.MethodGet, path+"?"+query.Encode(), nil, &batch)
		if err != nil {
			return nil, false, err
		}
		rows = append(rows, batch...)
		next := header.Get("X-Next-Page")
		if next == "" {
			// Older instances can omit the next-page header. A full page may
			// still have successors, so fetch the next page within our limit.
			if len(batch) < 100 {
				return rows, false, nil
			}
			if requestNumber == maxPages {
				return rows, true, nil
			}
			page++
			continue
		}
		nextPage, err := strconv.Atoi(next)
		if err != nil || nextPage <= page {
			return nil, false, errors.New("invalid GitLab pagination")
		}
		if requestNumber == maxPages {
			return rows, true, nil
		}
		page = nextPage
	}
	return rows, false, nil
}

type WorkbenchIssue struct {
	ProjectID   int64           `json:"project_id"`
	IID         int64           `json:"iid"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	State       string          `json:"state"`
	Labels      []string        `json:"labels"`
	Assignees   []WorkbenchUser `json:"assignees"`
	WebURL      string          `json:"web_url"`
	UpdatedAt   string          `json:"updated_at"`
}
type WorkbenchMR struct {
	ProjectID           int64           `json:"project_id"`
	IID                 int64           `json:"iid"`
	Title               string          `json:"title"`
	Description         string          `json:"description"`
	State               string          `json:"state"`
	Labels              []string        `json:"labels"`
	Assignees           []WorkbenchUser `json:"assignees"`
	WebURL              string          `json:"web_url"`
	UpdatedAt           string          `json:"updated_at"`
	SourceBranch        string          `json:"source_branch"`
	TargetBranch        string          `json:"target_branch"`
	SHA                 string          `json:"sha"`
	DetailedMergeStatus string          `json:"detailed_merge_status"`
	MergeStatus         string          `json:"merge_status"`
	HasConflicts        bool            `json:"has_conflicts"`
	Draft               bool            `json:"draft"`
}
type WorkbenchNote struct {
	ID        int64         `json:"id"`
	Body      string        `json:"body"`
	Author    WorkbenchUser `json:"author"`
	CreatedAt string        `json:"created_at"`
	System    bool          `json:"system"`
}
type WorkbenchDiscussion struct {
	ID             string `json:"id"`
	IndividualNote bool   `json:"individual_note"`
	Notes          []struct {
		WorkbenchNote
		Resolvable bool            `json:"resolvable"`
		Resolved   bool            `json:"resolved"`
		Position   json.RawMessage `json:"position,omitempty"`
	} `json:"notes"`
}
type WorkbenchDiff struct {
	OldPath       string `json:"old_path"`
	NewPath       string `json:"new_path"`
	Diff          string `json:"diff"`
	TooLarge      bool   `json:"too_large"`
	Collapsed     bool   `json:"collapsed"`
	GeneratedFile bool   `json:"generated_file"`
}
type WorkbenchDiffVersion struct {
	ID             int64           `json:"id"`
	BaseCommitSHA  string          `json:"base_commit_sha"`
	StartCommitSHA string          `json:"start_commit_sha"`
	HeadCommitSHA  string          `json:"head_commit_sha"`
	Diffs          []WorkbenchDiff `json:"diffs"`
	Overflow       bool            `json:"overflow"`
	State          string          `json:"state"`
}
type WorkbenchPipeline struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	WebURL string `json:"web_url"`
	Ref    string `json:"ref"`
	SHA    string `json:"sha"`
}
type WorkbenchApprovals struct {
	Approved      bool `json:"approved"`
	ApprovalsLeft int  `json:"approvals_left"`
	ApprovedBy    []struct {
		User WorkbenchUser `json:"user"`
	} `json:"approved_by"`
}

func workbenchPath(kind string, projectID, iid int64) string {
	return fmt.Sprintf("/projects/%d/%s/%d", projectID, kind, iid)
}
func (c *Client) WorkbenchPage(ctx context.Context, kind string, projectID int64, q url.Values, page, perPage int) ([]json.RawMessage, bool, error) {
	if kind != "issues" && kind != "merge_requests" {
		return nil, false, errors.New("invalid workbench kind")
	}
	if page < 1 || perPage < 1 || perPage > 100 {
		return nil, false, errors.New("invalid pagination")
	}
	query := url.Values{}
	for key, values := range q {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	query.Set("page", strconv.Itoa(page))
	query.Set("per_page", strconv.Itoa(perPage))
	query.Set("order_by", "updated_at")
	query.Set("sort", "desc")
	var rows []json.RawMessage
	h, err := c.request(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/%s?%s", projectID, kind, query.Encode()), nil, &rows)
	if err != nil {
		return nil, false, err
	}
	if rows == nil {
		rows = []json.RawMessage{}
	}
	return rows, h.Get("X-Next-Page") != "", nil
}
func (c *Client) WorkbenchGet(ctx context.Context, kind string, projectID, iid int64) (json.RawMessage, error) {
	var row json.RawMessage
	_, err := c.request(ctx, http.MethodGet, workbenchPath(kind, projectID, iid), nil, &row)
	return row, err
}
func (c *Client) WorkbenchWrite(ctx context.Context, method, kind string, projectID, iid int64, body any) (json.RawMessage, error) {
	path := fmt.Sprintf("/projects/%d/%s", projectID, kind)
	if iid > 0 {
		path = workbenchPath(kind, projectID, iid)
	}
	var row json.RawMessage
	_, err := c.request(ctx, method, path, body, &row)
	return row, err
}
func (c *Client) WorkbenchSubresource(ctx context.Context, method, kind string, projectID, iid int64, suffix string, body any) (json.RawMessage, error) {
	var row json.RawMessage
	_, err := c.request(ctx, method, workbenchPath(kind, projectID, iid)+suffix, body, &row)
	return row, err
}
func (c *Client) WorkbenchList(ctx context.Context, kind string, projectID, iid int64, suffix string) ([]json.RawMessage, error) {
	return paginate[json.RawMessage](ctx, c, workbenchPath(kind, projectID, iid)+suffix, nil)
}
func (c *Client) WorkbenchDiffPage(ctx context.Context, projectID, iid int64, page int) ([]WorkbenchDiff, bool, WorkbenchDiffVersion, error) {
	var versions []WorkbenchDiffVersion
	_, err := c.request(ctx, http.MethodGet, workbenchPath("merge_requests", projectID, iid)+"/versions?per_page=1", nil, &versions)
	if err != nil {
		return nil, false, WorkbenchDiffVersion{}, err
	}
	if len(versions) == 0 {
		return []WorkbenchDiff{}, false, WorkbenchDiffVersion{}, nil
	}
	if page < 1 {
		page = 1
	}
	q := url.Values{"page": {strconv.Itoa(page)}, "per_page": {"20"}}
	var diffs []WorkbenchDiff
	header, err := c.request(ctx, http.MethodGet, workbenchPath("merge_requests", projectID, iid)+"/diffs?"+q.Encode(), nil, &diffs)
	if err != nil {
		return nil, false, WorkbenchDiffVersion{}, err
	}
	if diffs == nil {
		diffs = []WorkbenchDiff{}
	}
	return diffs, header.Get("X-Next-Page") != "", versions[0], nil
}
func (c *Client) WorkbenchUpload(ctx context.Context, projectID int64, filename string, file io.Reader) (json.RawMessage, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err = io.Copy(part, io.LimitReader(file, 20<<20+1)); err != nil {
		return nil, err
	}
	if buf.Len() > 20<<20+4096 {
		return nil, errors.New("upload exceeds 20 MiB limit")
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/projects/%d/uploads", c.baseURL, projectID), &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req, span := observability.TraceOutboundHTTP(req, "gitlab")
	defer span.End()
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{StatusCode: resp.StatusCode, Reason: gitlabErrorReason(resp.Body, c.token)}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("upload response exceeds size limit")
	}
	if !json.Valid(data) {
		return nil, errors.New("invalid upload response")
	}
	return data, nil
}

func ValidDiscussionID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return !strings.Contains(id, "..")
}
