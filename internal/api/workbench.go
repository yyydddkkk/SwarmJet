package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/gitlab"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

type workbenchRepository struct {
	Project domain.Project
	Repo    domain.Repository
}

func (a *server) gitlabIdentities(w http.ResponseWriter, r *http.Request) {
	if a.s == nil || a.cfg.Box == nil {
		problem(w, 503, "请先配置 SWARMJET_MASTER_KEY 以保存个人 GitLab Token。")
		return
	}
	switch r.Method {
	case http.MethodGet:
		projects, err := a.s.Projects(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		visible, err := a.visibleProjects(r)
		if err != nil {
			failure(w, err)
			return
		}
		seen := map[string]bool{}
		out := []map[string]any{}
		for _, p := range projects {
			if visible != nil {
				if _, ok := visible[p.ID]; !ok {
					continue
				}
			}
			if p.HostingProvider() != domain.ProviderGitLab || p.GitLabURL == "" || seen[p.GitLabURL] {
				continue
			}
			seen[p.GitLabURL] = true
			_, username, _, err := a.workbenchSecret(r.Context(), p.GitLabURL)
			out = append(out, map[string]any{"gitlabUrl": p.GitLabURL, "username": username, "configured": err == nil})
		}
		respond(w, 200, map[string]any{"identities": out})
	case http.MethodPut:
		var req struct {
			GitLabURL string `json:"gitlabUrl"`
			Token     string `json:"token"`
		}
		if !decode(w, r, &req) {
			return
		}
		base, err := domain.NormalizeGitLabURL(req.GitLabURL)
		if err != nil {
			problem(w, 400, err.Error())
			return
		}
		if len(req.Token) == 0 || len(req.Token) > 512 {
			problem(w, 400, "Token 长度无效。")
			return
		}
		if !a.knownGitLabInstance(r.Context(), base) {
			problem(w, 404, "此 GitLab 实例未接入项目。")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		user, err := gitlab.New(base, req.Token).CurrentUser(ctx)
		if err != nil {
			gitlabProblem(w, err)
			return
		}
		sealed, err := a.cfg.Box.Seal(req.Token)
		if err != nil {
			failure(w, err)
			return
		}
		principal := requestPrincipal(r)
		if principal.Breakglass {
			err = a.s.SaveGitLabIdentity(r.Context(), base, sealed, user.Username)
		} else {
			err = a.s.UpsertExternalIdentity(r.Context(), principal.UserID, "gitlab", base, strconv.FormatInt(user.ID, 10), user.Username, sealed)
		}
		if err != nil {
			failure(w, err)
			return
		}
		_ = a.s.Audit(r.Context(), principal, "identity.gitlab.bind", "gitlab", base, map[string]string{"externalId": strconv.FormatInt(user.ID, 10)})
		respond(w, 200, map[string]any{"gitlabUrl": base, "username": user.Username, "configured": true})
	case http.MethodDelete:
		base, err := domain.NormalizeGitLabURL(r.URL.Query().Get("gitlabUrl"))
		if err != nil {
			problem(w, 400, "GitLab 地址无效。")
			return
		}
		principal := requestPrincipal(r)
		if principal.Breakglass {
			err = a.s.DeleteGitLabIdentity(r.Context(), base)
		} else {
			err = a.s.DeleteExternalIdentity(r.Context(), principal.UserID, "gitlab", base)
		}
		if err != nil {
			failure(w, err)
			return
		}
		_ = a.s.Audit(r.Context(), principal, "identity.gitlab.unbind", "gitlab", base, nil)
		w.WriteHeader(204)
	default:
		problem(w, 405, "method not allowed")
	}
}

func (a *server) knownGitLabInstance(ctx context.Context, base string) bool {
	if a.s == nil {
		return false
	}
	projects, err := a.s.Projects(ctx)
	if err != nil {
		return false
	}
	for _, p := range projects {
		principal, _ := ctx.Value(principalContextKey{}).(store.Principal)
		if !principal.PlatformAdmin && !principal.Breakglass {
			role, err := a.s.ProjectRole(ctx, principal.UserID, p.ID)
			if err != nil || role == "" {
				continue
			}
		}
		if p.HostingProvider() == domain.ProviderGitLab && domain.GitLabIdentity(p.GitLabURL) == domain.GitLabIdentity(base) {
			return true
		}
	}
	return false
}

func (a *server) workbenchSecret(ctx context.Context, instance string) (externalID, username, secret string, err error) {
	principal, _ := ctx.Value(principalContextKey{}).(store.Principal)
	if principal.Breakglass || principal.UserID == "" {
		secret, username, err = a.s.GitLabIdentity(ctx, instance)
		return "", username, secret, err
	}
	return a.s.ExternalIdentitySecret(ctx, principal.UserID, "gitlab", instance)
}

func (a *server) workbenchClient(ctx context.Context, p domain.Project) (*gitlab.Client, error) {
	if a.cfg.Box == nil {
		return nil, errors.New("个人 GitLab Token 存储未启用，请配置 SWARMJET_MASTER_KEY。")
	}
	_, _, sealed, err := a.workbenchSecret(ctx, p.GitLabURL)
	if err != nil {
		return nil, errors.New("请先在系统设置中为此 GitLab 实例配置个人 Token。")
	}
	token, err := a.cfg.Box.Open(sealed)
	if err != nil || token == "" {
		return nil, errors.New("个人 GitLab Token 无法解密，请重新设置。")
	}
	return gitlab.New(p.GitLabURL, token), nil
}

func (a *server) workbenchRepos(ctx context.Context, projectID, repoID string) ([]workbenchRepository, error) {
	if projectID != "" && !domain.IDPattern.MatchString(projectID) {
		return nil, errors.New("项目筛选无效")
	}
	if repoID != "" && !domain.IDPattern.MatchString(repoID) {
		return nil, errors.New("仓库筛选无效")
	}
	projects, err := a.s.Projects(ctx)
	if err != nil {
		return nil, err
	}
	principal, _ := ctx.Value(principalContextKey{}).(store.Principal)
	if !principal.PlatformAdmin && !principal.Breakglass && principal.UserID != "" {
		visible, err := a.s.VisibleProjectIDs(ctx, principal.UserID)
		if err != nil {
			return nil, err
		}
		allowed := projects[:0]
		for _, p := range projects {
			if _, ok := visible[p.ID]; ok {
				allowed = append(allowed, p)
			}
		}
		projects = allowed
	}
	return filterWorkbenchRepos(projects, projectID, repoID), nil
}

func filterWorkbenchRepos(projects []domain.Project, projectID, repoID string) []workbenchRepository {
	out := []workbenchRepository{}
	for _, p := range projects {
		if p.HostingProvider() != domain.ProviderGitLab || projectID != "" && p.ID != projectID {
			continue
		}
		for _, repo := range p.Repositories {
			if repo.ProjectID > 0 && (repoID == "" || repo.ID == repoID) {
				out = append(out, workbenchRepository{p, repo})
			}
		}
	}
	return out
}

func (a *server) workbenchScope(w http.ResponseWriter, r *http.Request, projectID, repoID string) (workbenchRepository, *gitlab.Client, bool) {
	minimum := store.RoleViewer
	if r.Method != http.MethodGet {
		minimum = store.RoleOperator
	}
	if !a.requireProjectRole(w, r, projectID, minimum) {
		return workbenchRepository{}, nil, false
	}
	repos, err := a.workbenchRepos(r.Context(), projectID, repoID)
	if err != nil {
		problem(w, 400, err.Error())
		return workbenchRepository{}, nil, false
	}
	if len(repos) != 1 || repos[0].Project.ID != projectID || repos[0].Repo.ID != repoID {
		problem(w, 404, "项目仓库不存在或不属于此 GitLab 实例。")
		return workbenchRepository{}, nil, false
	}
	c, err := a.workbenchClient(r.Context(), repos[0].Project)
	if err != nil {
		problem(w, 503, err.Error())
		return workbenchRepository{}, nil, false
	}
	return repos[0], c, true
}

type workbenchCursor struct {
	Repo   int `json:"repo"`
	Page   int `json:"page"`
	Offset int `json:"offset,omitempty"`
}

// Only upstream list pages are cached. Issue/MR details and local Task links are
// fetched for every request; edits clear this cache immediately.
const workbenchPageTTL = 8 * time.Second

type workbenchPageKey struct {
	identity [32]byte
	project  int64
	kind     string
	filters  string
	page     int
}
type workbenchPageEntry struct {
	rows    []json.RawMessage
	more    bool
	expires time.Time
}
type workbenchPageCache struct {
	mu         sync.Mutex
	generation uint64
	entries    map[workbenchPageKey]workbenchPageEntry
}
type workbenchIdentityError string

func (e workbenchIdentityError) Error() string { return string(e) }

func (a *server) invalidateWorkbenchPages() {
	a.workbenchPages.mu.Lock()
	a.workbenchPages.generation++
	a.workbenchPages.entries = nil
	a.workbenchPages.mu.Unlock()
}

func (a *server) fetchWorkbenchPage(ctx context.Context, scope workbenchRepository, kind string, filters url.Values, page int) ([]json.RawMessage, bool, error) {
	if a.cfg.Box == nil {
		return nil, false, workbenchIdentityError("个人 GitLab Token 存储未启用，请配置 SWARMJET_MASTER_KEY。")
	}
	_, _, sealed, err := a.workbenchSecret(ctx, scope.Project.GitLabURL)
	if err != nil {
		return nil, false, workbenchIdentityError("请先在系统设置中为此 GitLab 实例配置个人 Token。")
	}
	key := workbenchPageKey{identity: sha256.Sum256([]byte(sealed)), project: scope.Repo.ProjectID, kind: kind, filters: filters.Encode(), page: page}
	cache := &a.workbenchPages
	cache.mu.Lock()
	generation := cache.generation
	if entry, ok := cache.entries[key]; ok && time.Now().Before(entry.expires) {
		cache.mu.Unlock()
		return entry.rows, entry.more, nil
	}
	cache.mu.Unlock()
	token, err := a.cfg.Box.Open(sealed)
	if err != nil || token == "" {
		return nil, false, workbenchIdentityError("个人 GitLab Token 无法解密，请重新设置。")
	}
	rows, more, err := gitlab.New(scope.Project.GitLabURL, token).WorkbenchPage(ctx, kind, scope.Repo.ProjectID, filters, page, 50)
	if err != nil {
		return nil, false, err
	}
	cache.mu.Lock()
	if cache.generation == generation {
		if cache.entries == nil {
			cache.entries = make(map[workbenchPageKey]workbenchPageEntry)
		}
		if len(cache.entries) >= 256 {
			for k, entry := range cache.entries {
				if time.Now().After(entry.expires) {
					delete(cache.entries, k)
				}
			}
			if len(cache.entries) >= 256 {
				cache.entries = make(map[workbenchPageKey]workbenchPageEntry)
			}
		}
		cache.entries[key] = workbenchPageEntry{rows: rows, more: more, expires: time.Now().Add(workbenchPageTTL)}
	}
	cache.mu.Unlock()
	return rows, more, nil
}

type workbenchPageResult struct {
	rows []json.RawMessage
	more bool
	err  error
}

// Repositories keep their configured display order, while their GitLab page
// requests run concurrently. Workers stop when the HTTP request ends.
func (a *server) prefetchWorkbenchPages(ctx context.Context, repos []workbenchRepository, start workbenchCursor, kind string, filters url.Values) []chan workbenchPageResult {
	results := make([]chan workbenchPageResult, len(repos))
	if start.Repo == len(repos) {
		return results
	}
	for i := start.Repo; i < len(repos); i++ {
		results[i] = make(chan workbenchPageResult, 1)
	}
	jobs := make(chan int)
	workers := len(repos) - start.Repo
	if workers > 6 {
		workers = 6
	}
	for n := 0; n < workers; n++ {
		go func() {
			for i := range jobs {
				page := 1
				if i == start.Repo {
					page = start.Page
				}
				rows, more, err := a.fetchWorkbenchPage(ctx, repos[i], kind, filters, page)
				select {
				case results[i] <- workbenchPageResult{rows: rows, more: more, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := start.Repo; i < len(repos); i++ {
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	return results
}

func parseWorkbenchCursor(raw string) (workbenchCursor, error) {
	if raw == "" {
		return workbenchCursor{Page: 1}, nil
	}
	var c workbenchCursor
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) > 128 || json.Unmarshal(data, &c) != nil || c.Repo < 0 || c.Page < 1 || c.Page > 10000 || c.Offset < 0 || c.Offset >= 50 {
		return c, errors.New("列表游标无效")
	}
	return c, nil
}
func encodeWorkbenchCursor(c workbenchCursor) string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(data)
}
func workbenchRow(raw json.RawMessage, scope workbenchRepository) map[string]any {
	row := map[string]any{}
	_ = json.Unmarshal(raw, &row)
	row["projectId"] = scope.Project.ID
	row["repositoryId"] = scope.Repo.ID
	row["gitlabProjectId"] = scope.Repo.ProjectID
	if value, ok := row["web_url"]; ok {
		row["webUrl"] = value
	}
	if value, ok := row["updated_at"]; ok {
		row["updatedAt"] = value
	}
	if value, ok := row["source_branch"]; ok {
		row["sourceBranch"] = value
	}
	if value, ok := row["target_branch"]; ok {
		row["targetBranch"] = value
	}
	if value, ok := row["detailed_merge_status"]; ok {
		row["detailedMergeStatus"] = value
	}
	return row
}
func workbenchError(w http.ResponseWriter, err error) {
	var apiErr *gitlab.APIError
	reason := ""
	if errors.As(err, &apiErr) && apiErr.Reason != "" {
		reason = "：" + apiErr.Reason
	}
	if status, ok := gitlab.StatusCode(err); ok {
		switch status {
		case 401:
			problem(w, 401, "个人 GitLab Token 无效或已过期，请在系统设置中更新。")
			return
		case 403:
			problem(w, 403, "当前 GitLab 身份没有执行此操作的权限"+reason)
			return
		case 404:
			problem(w, 404, "GitLab 未找到此资源或当前身份无权访问。")
			return
		case 409:
			problem(w, 409, "GitLab 拒绝此操作：资源状态已变化，请刷新后重试"+reason)
			return
		case 400, 422:
			problem(w, status, "GitLab 拒绝此操作"+reason)
			return
		case 405, 501:
			problem(w, 502, "此 GitLab 实例不支持该操作，请在 GitLab 原始页面操作。")
			return
		default:
			problem(w, 502, fmt.Sprintf("GitLab 返回 HTTP %d，请在原始页面检查原因。", status))
			return
		}
	}
	problem(w, 502, "GitLab 请求失败："+err.Error())
}

func (a *server) workbench(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/workbench/"), "/")
	if len(parts) == 0 || parts[0] != "issues" && parts[0] != "merge-requests" {
		problem(w, 404, "API route not found")
		return
	}
	kind := "issues"
	if parts[0] == "merge-requests" {
		kind = "merge_requests"
	}
	if len(parts) == 1 {
		if r.Method == http.MethodGet {
			a.workbenchPage(w, r, kind)
			return
		}
		if r.Method == http.MethodPost {
			a.workbenchCreate(w, r, kind)
			return
		}
		problem(w, 405, "method not allowed")
		return
	}
	if len(parts) < 4 {
		problem(w, 404, "API route not found")
		return
	}
	if len(parts) == 4 && parts[3] == "options" {
		a.workbenchOptions(w, r, parts[1], parts[2])
		return
	}
	iid, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || iid < 1 {
		problem(w, 400, "Issue/MR 编号无效")
		return
	}
	scope, client, ok := a.workbenchScope(w, r, parts[1], parts[2])
	if !ok {
		return
	}
	if len(parts) == 4 {
		a.workbenchDetail(w, r, kind, scope, client, iid)
		return
	}
	a.workbenchAction(w, r, kind, scope, client, iid, parts[4:])
}

func (a *server) workbenchOptions(w http.ResponseWriter, r *http.Request, projectID, repoID string) {
	if r.Method != http.MethodGet {
		problem(w, 405, "method not allowed")
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	if len(search) > 100 {
		problem(w, 400, "负责人搜索条件过长")
		return
	}
	scope, client, ok := a.workbenchScope(w, r, projectID, repoID)
	if !ok {
		return
	}
	labels, assignees, labelsTruncated, assigneesTruncated, err := client.WorkbenchOptions(r.Context(), scope.Repo.ProjectID, search)
	if err != nil {
		workbenchError(w, err)
		return
	}
	respond(w, 200, map[string]any{"labels": labels, "assignees": assignees, "labelsTruncated": labelsTruncated, "assigneesTruncated": assigneesTruncated})
}

func (a *server) workbenchPage(w http.ResponseWriter, r *http.Request, kind string) {
	q := r.URL.Query()
	repos, err := a.workbenchRepos(r.Context(), q.Get("projectId"), q.Get("repositoryId"))
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	cursor, err := parseWorkbenchCursor(q.Get("cursor"))
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	if cursor.Repo > len(repos) {
		problem(w, 400, "列表游标无效")
		return
	}
	filters := url.Values{}
	for _, key := range []string{"state", "labels", "search", "assignee_id"} {
		if v := q.Get(key); v != "" {
			filters.Set(key, v)
		}
	}
	if v := q.Get("assigneeId"); v != "" {
		filters.Set("assignee_id", v)
	}
	if state := filters.Get("state"); state != "" && state != "all" && state != "opened" && state != "closed" && state != "merged" {
		problem(w, 400, "状态筛选无效")
		return
	}
	const pageSize = 50
	items := []map[string]any{}
	next := ""
	prefetched := a.prefetchWorkbenchPages(r.Context(), repos, cursor, kind, filters)
	for i := cursor.Repo; i < len(repos) && len(items) < pageSize; i++ {
		page := 1
		offset := 0
		if i == cursor.Repo {
			page = cursor.Page
			offset = cursor.Offset
		}
		for len(items) < pageSize {
			var rows []json.RawMessage
			var more bool
			if prefetched[i] != nil {
				select {
				case fetched := <-prefetched[i]:
					rows, more, err = fetched.rows, fetched.more, fetched.err
				case <-r.Context().Done():
					return
				}
				prefetched[i] = nil
			} else {
				rows, more, err = a.fetchWorkbenchPage(r.Context(), repos[i], kind, filters, page)
			}
			if err != nil {
				var identityErr workbenchIdentityError
				if errors.As(err, &identityErr) {
					problem(w, 503, err.Error())
					return
				}
				workbenchError(w, err)
				return
			}
			if offset > len(rows) {
				problem(w, 400, "列表游标已失效，请重新筛选。")
				return
			}
			remaining := pageSize - len(items)
			end := len(rows)
			if offset+remaining < end {
				end = offset + remaining
			}
			for _, raw := range rows[offset:end] {
				item := workbenchRow(raw, repos[i])
				iid, _ := item["iid"].(float64)
				if kind == "issues" {
					tasks, err := a.s.TasksForIssueInProject(r.Context(), repos[i].Project.ID, repos[i].Project.GitLabURL, domain.Issue{ProjectID: repos[i].Repo.ProjectID, IID: int64(iid)})
					if err != nil {
						failure(w, err)
						return
					}
					ids := []string{}
					for _, t := range tasks {
						ids = append(ids, t.ID)
					}
					item["taskIds"] = ids
				} else {
					task, err := a.s.TaskForMRInProject(r.Context(), repos[i].Project.ID, repos[i].Project.GitLabURL, repos[i].Repo.ProjectID, int64(iid))
					if err == nil {
						item["taskId"] = task.ID
					} else if !errors.Is(err, store.ErrNotFound) {
						failure(w, err)
						return
					}
				}
				items = append(items, item)
			}
			if end < len(rows) {
				next = encodeWorkbenchCursor(workbenchCursor{Repo: i, Page: page, Offset: end})
				break
			}
			if more {
				page++
				offset = 0
				if len(items) == pageSize {
					next = encodeWorkbenchCursor(workbenchCursor{Repo: i, Page: page})
					break
				}
				continue
			}
			next = ""
			if i+1 < len(repos) {
				next = encodeWorkbenchCursor(workbenchCursor{Repo: i + 1, Page: 1})
			}
			break
		}
	}
	respond(w, 200, map[string]any{"items": items, "nextCursor": next})
}

func (a *server) workbenchCreate(w http.ResponseWriter, r *http.Request, kind string) {
	var req map[string]json.RawMessage
	if !decode(w, r, &req) {
		return
	}
	var projectID, repoID string
	_ = json.Unmarshal(req["projectId"], &projectID)
	_ = json.Unmarshal(req["repositoryId"], &repoID)
	scope, client, ok := a.workbenchScope(w, r, projectID, repoID)
	if !ok {
		return
	}
	body := map[string]any{}
	allowed := map[string]string{"title": "title", "description": "description", "labels": "labels", "assigneeIds": "assignee_ids"}
	if kind == "merge_requests" {
		allowed["sourceBranch"] = "source_branch"
		allowed["targetBranch"] = "target_branch"
		allowed["removeSourceBranch"] = "remove_source_branch"
	}
	for from, to := range allowed {
		if value, ok := req[from]; ok {
			var decoded any
			if json.Unmarshal(value, &decoded) == nil {
				if from == "labels" {
					decoded = labelsString(decoded)
				}
				body[to] = decoded
			}
		}
	}
	if strings.TrimSpace(stringValue(body["title"])) == "" {
		problem(w, 400, "标题不能为空")
		return
	}
	if kind == "merge_requests" && (stringValue(body["source_branch"]) == "" || stringValue(body["target_branch"]) == "") {
		problem(w, 400, "源分支和目标分支不能为空")
		return
	}
	row, err := client.WorkbenchWrite(r.Context(), http.MethodPost, kind, scope.Repo.ProjectID, 0, body)
	if err != nil {
		workbenchError(w, err)
		return
	}
	a.invalidateWorkbenchPages()
	_ = a.s.Audit(r.Context(), requestPrincipal(r), "workbench."+kind+".create", "project", scope.Project.ID, map[string]string{"repositoryId": scope.Repo.ID})
	respond(w, 201, workbenchRow(row, scope))
}
func stringValue(v any) string { s, _ := v.(string); return s }
func labelsString(v any) any {
	if values, ok := v.([]any); ok {
		labels := make([]string, 0, len(values))
		for _, value := range values {
			if label, ok := value.(string); ok {
				labels = append(labels, label)
			}
		}
		return strings.Join(labels, ",")
	}
	return v
}

func (a *server) workbenchDetail(w http.ResponseWriter, r *http.Request, kind string, scope workbenchRepository, client *gitlab.Client, iid int64) {
	if r.Method == http.MethodGet {
		row, err := client.WorkbenchGet(r.Context(), kind, scope.Repo.ProjectID, iid)
		if err != nil {
			workbenchError(w, err)
			return
		}
		item := workbenchRow(row, scope)
		if kind == "issues" {
			notes, err := client.WorkbenchList(r.Context(), kind, scope.Repo.ProjectID, iid, "/notes")
			if err != nil {
				workbenchError(w, err)
				return
			}
			item["notes"] = notes
			tasks, err := a.s.TasksForIssueInProject(r.Context(), scope.Project.ID, scope.Project.GitLabURL, domain.Issue{ProjectID: scope.Repo.ProjectID, IID: iid})
			if err != nil {
				failure(w, err)
				return
			}
			item["tasks"] = tasks
		} else {
			task, err := a.s.TaskForMRInProject(r.Context(), scope.Project.ID, scope.Project.GitLabURL, scope.Repo.ProjectID, iid)
			if err == nil {
				item["taskId"] = task.ID
			} else if !errors.Is(err, store.ErrNotFound) {
				failure(w, err)
				return
			}
			discussions, err := client.WorkbenchList(r.Context(), kind, scope.Repo.ProjectID, iid, "/discussions")
			if err != nil {
				workbenchError(w, err)
				return
			}
			item["discussions"] = discussions
			pipelines, err := client.WorkbenchList(r.Context(), kind, scope.Repo.ProjectID, iid, "/pipelines")
			if err == nil {
				item["pipelines"] = pipelines
			} else {
				item["pipelinesError"] = "GitLab CI 信息不可用"
			}
			approvals, err := client.WorkbenchSubresource(r.Context(), http.MethodGet, kind, scope.Repo.ProjectID, iid, "/approvals", nil)
			if err == nil {
				item["approvals"] = approvals
				_, username, _, identityErr := a.workbenchSecret(r.Context(), scope.Project.GitLabURL)
				if identityErr != nil {
					failure(w, identityErr)
					return
				}
				var state gitlab.WorkbenchApprovals
				if json.Unmarshal(approvals, &state) == nil {
					approved := false
					for _, entry := range state.ApprovedBy {
						approved = approved || entry.User.Username == username
					}
					item["approvedByCurrentUser"] = approved
				}
			} else {
				item["approvalsError"] = "GitLab 批准信息不可用"
			}
		}
		respond(w, 200, item)
		return
	}
	if r.Method != http.MethodPut {
		problem(w, 405, "method not allowed")
		return
	}
	var req map[string]json.RawMessage
	if !decode(w, r, &req) {
		return
	}
	body := map[string]any{}
	allowed := map[string]string{"title": "title", "description": "description", "labels": "labels", "assigneeIds": "assignee_ids", "stateEvent": "state_event"}
	if kind == "merge_requests" {
		allowed["targetBranch"] = "target_branch"
		allowed["removeSourceBranch"] = "remove_source_branch"
	}
	for from, to := range allowed {
		if value, ok := req[from]; ok {
			var decoded any
			if json.Unmarshal(value, &decoded) == nil {
				if from == "labels" {
					decoded = labelsString(decoded)
				}
				body[to] = decoded
			}
		}
	}
	if state := stringValue(body["state_event"]); state != "" && state != "close" && state != "reopen" {
		problem(w, 400, "状态操作无效")
		return
	}
	if kind == "issues" && body["state_event"] == "close" {
		tasks, err := a.s.TasksForIssue(r.Context(), scope.Project.GitLabURL, domain.Issue{ProjectID: scope.Repo.ProjectID, IID: iid})
		if err != nil {
			failure(w, err)
			return
		}
		for _, task := range tasks {
			if !domain.Terminal(task.Stage) {
				problem(w, 409, "关联 Task 仍在活动中，暂不能关闭 Issue。")
				return
			}
		}
	}
	row, err := client.WorkbenchWrite(r.Context(), http.MethodPut, kind, scope.Repo.ProjectID, iid, body)
	if err != nil {
		workbenchError(w, err)
		return
	}
	a.invalidateWorkbenchPages()
	respond(w, 200, workbenchRow(row, scope))
}

func (a *server) workbenchAction(w http.ResponseWriter, r *http.Request, kind string, scope workbenchRepository, client *gitlab.Client, iid int64, parts []string) {
	if len(parts) == 3 && parts[0] == "uploads" && r.Method == http.MethodGet {
		a.workbenchImage(w, r, client, scope.Repo.ProjectID, parts[1], parts[2])
		return
	}
	if kind == "issues" && len(parts) == 1 && parts[0] == "next-round" && r.Method == http.MethodPost {
		tasks, err := a.s.TasksForIssueInProject(r.Context(), scope.Project.ID, scope.Project.GitLabURL, domain.Issue{ProjectID: scope.Repo.ProjectID, IID: iid})
		if err != nil {
			failure(w, err)
			return
		}
		if len(tasks) == 0 || !domain.Terminal(tasks[len(tasks)-1].Stage) {
			problem(w, 409, "上一轮 Task 尚未结束，或此 Issue 尚无 Task。")
			return
		}
		body, _ := json.Marshal(map[string]any{"projectId": scope.Project.ID, "issueProjectId": scope.Repo.ProjectID, "issueIid": iid})
		clone := r.Clone(r.Context())
		clone.Body = io.NopCloser(bytes.NewReader(body))
		a.tasks(w, clone)
		return
	}
	if len(parts) == 1 && parts[0] == "uploads" && kind == "issues" && r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 20<<20+4096)
		file, header, err := r.FormFile("file")
		if err != nil {
			problem(w, 400, "请选择 20 MiB 以内的附件。")
			return
		}
		defer file.Close()
		row, err := client.WorkbenchUpload(r.Context(), scope.Repo.ProjectID, header.Filename, file)
		if err != nil {
			workbenchError(w, err)
			return
		}
		respond(w, 201, row)
		return
	}
	if len(parts) == 1 && parts[0] == "diffs" && kind == "merge_requests" && r.Method == http.MethodGet {
		page := 1
		if raw := r.URL.Query().Get("page"); raw != "" {
			var err error
			page, err = strconv.Atoi(raw)
			if err != nil || page < 1 || page > 10000 {
				problem(w, 400, "差异页码无效")
				return
			}
		}
		diffs, more, version, err := client.WorkbenchDiffPage(r.Context(), scope.Repo.ProjectID, iid, page)
		if err != nil {
			workbenchError(w, err)
			return
		}
		incomplete := version.Overflow || version.State == "overflow" || version.State == "without_files"
		for _, diff := range version.Diffs {
			incomplete = incomplete || diff.TooLarge || diff.Collapsed
		}
		respond(w, 200, map[string]any{"diffs": diffs, "hasNext": more, "version": map[string]any{"id": version.ID, "baseCommitSha": version.BaseCommitSHA, "startCommitSha": version.StartCommitSHA, "headCommitSha": version.HeadCommitSHA}, "incomplete": incomplete, "page": page})
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		problem(w, 405, "method not allowed")
		return
	}
	if len(parts) == 1 && parts[0] == "notes" {
		var req struct {
			Body           string `json:"body"`
			StartNextRound bool   `json:"startNextRound"`
		}
		if !decode(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Body) == "" {
			problem(w, 400, "评论不能为空")
			return
		}
		if req.StartNextRound && kind != "issues" {
			problem(w, 400, "只有 Issue 评论可以发起下一轮")
			return
		}
		body := req.Body
		if req.StartNextRound {
			body = "/swarmjet run " + body
		}
		row, err := client.WorkbenchSubresource(r.Context(), http.MethodPost, kind, scope.Repo.ProjectID, iid, "/notes", map[string]any{"body": body})
		if err != nil {
			workbenchError(w, err)
			return
		}
		a.invalidateWorkbenchPages()
		respond(w, 201, row)
		return
	}
	if kind != "merge_requests" {
		problem(w, 404, "API route not found")
		return
	}
	if len(parts) == 1 && parts[0] == "discussions" {
		var req struct {
			Body     string          `json:"body"`
			Position json.RawMessage `json:"position"`
		}
		if !decode(w, r, &req) {
			return
		}
		if strings.TrimSpace(req.Body) == "" {
			problem(w, 400, "讨论内容不能为空")
			return
		}
		body := map[string]any{"body": req.Body}
		if len(req.Position) > 0 {
			var pos map[string]any
			if json.Unmarshal(req.Position, &pos) != nil {
				problem(w, 400, "逐行位置无效")
				return
			}
			position := map[string]any{}
			for _, key := range []string{"base_sha", "start_sha", "head_sha", "position_type", "old_path", "new_path", "old_line", "new_line"} {
				if value, ok := pos[key]; ok {
					position[key] = value
				}
			}
			body["position"] = position
		}
		row, err := client.WorkbenchSubresource(r.Context(), http.MethodPost, kind, scope.Repo.ProjectID, iid, "/discussions", body)
		if err != nil {
			workbenchError(w, err)
			return
		}
		a.invalidateWorkbenchPages()
		respond(w, 201, row)
		return
	}
	if len(parts) == 3 && parts[0] == "discussions" && gitlab.ValidDiscussionID(parts[1]) {
		if parts[2] == "notes" {
			var req struct {
				Body string `json:"body"`
			}
			if !decode(w, r, &req) {
				return
			}
			if strings.TrimSpace(req.Body) == "" {
				problem(w, 400, "回复不能为空")
				return
			}
			row, err := client.WorkbenchSubresource(r.Context(), http.MethodPost, kind, scope.Repo.ProjectID, iid, "/discussions/"+parts[1]+"/notes", map[string]any{"body": req.Body})
			if err != nil {
				workbenchError(w, err)
				return
			}
			a.invalidateWorkbenchPages()
			respond(w, 201, row)
			return
		}
		if parts[2] == "resolve" {
			var req struct {
				Resolved bool `json:"resolved"`
			}
			if !decode(w, r, &req) {
				return
			}
			row, err := client.WorkbenchSubresource(r.Context(), http.MethodPut, kind, scope.Repo.ProjectID, iid, "/discussions/"+parts[1], map[string]any{"resolved": req.Resolved})
			if err != nil {
				workbenchError(w, err)
				return
			}
			a.invalidateWorkbenchPages()
			respond(w, 200, row)
			return
		}
	}
	if len(parts) == 1 && (parts[0] == "approve" || parts[0] == "unapprove" || parts[0] == "merge") {
		var req struct {
			SHA string `json:"sha"`
		}
		if !decode(w, r, &req) {
			return
		}
		current, err := client.WorkbenchGet(r.Context(), kind, scope.Repo.ProjectID, iid)
		if err != nil {
			workbenchError(w, err)
			return
		}
		var mr gitlab.WorkbenchMR
		if json.Unmarshal(current, &mr) != nil {
			problem(w, 502, "GitLab MR 数据无效")
			return
		}
		if req.SHA == "" || req.SHA != mr.SHA {
			problem(w, 409, "MR HEAD 已变化，请刷新后重新审查。")
			return
		}
		if parts[0] == "merge" {
			if _, err := a.s.TaskForMR(r.Context(), scope.Project.GitLabURL, scope.Repo.ProjectID, iid); err == nil {
				problem(w, 409, "托管 MR 请从关联 Task 合并整轮。")
				return
			} else if !errors.Is(err, store.ErrNotFound) {
				failure(w, err)
				return
			}
			if mr.State != "opened" || mr.Draft || mr.HasConflicts || mr.DetailedMergeStatus != "mergeable" {
				problem(w, 409, "GitLab 尚未允许合并，请刷新并检查 CI、批准和冲突。")
				return
			}
		}
		suffix := "/" + parts[0]
		var body any = map[string]any{}
		if parts[0] == "approve" {
			body = map[string]any{"sha": req.SHA}
		}
		if parts[0] == "merge" {
			body = map[string]any{"sha": req.SHA, "merge_when_pipeline_succeeds": false}
		}
		method := http.MethodPost
		if parts[0] == "merge" {
			method = http.MethodPut
		}
		row, err := client.WorkbenchSubresource(r.Context(), method, kind, scope.Repo.ProjectID, iid, suffix, body)
		if err != nil {
			workbenchError(w, err)
			return
		}
		a.invalidateWorkbenchPages()
		respond(w, 200, row)
		return
	}
	problem(w, 404, "API route not found")
}
