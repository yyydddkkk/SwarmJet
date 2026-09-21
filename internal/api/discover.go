package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"swarmjet/internal/domain"
)

type discoverRequest struct {
	MetaURL             string `json:"metaUrl"`
	MetaRef             string `json:"metaRef"`
	GitLabURL           string `json:"gitlabUrl"`
	CredentialRef       string `json:"credentialRef"`
	DefaultTargetBranch string `json:"defaultTargetBranch"`
}
type discoverResponse struct {
	Name         string              `json:"name"`
	SuggestedID  string              `json:"suggestedId"`
	GitLabURL    string              `json:"gitlabUrl"`
	MetaURL      string              `json:"metaUrl"`
	MetaRef      string              `json:"metaRef"`
	Repositories []domain.Repository `json:"repositories"`
	Branches     map[string][]string `json:"branches"`
	Warnings     []string            `json:"warnings"`
}
type discoveryProject struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	HTTPURL       string `json:"http_url_to_repo"`
}
type discoveryEntry struct {
	Alias, URL, Path, ID, ProjectPath string
	DependsOn, Provides               []string
}
type discoveryHTTPError struct{ Code int }

func (e *discoveryHTTPError) Error() string { return fmt.Sprintf("GitLab 返回 HTTP %d", e.Code) }

type discoveryClient struct {
	base, token string
	http        *http.Client
}

func (c discoveryClient) get(ctx context.Context, endpoint string, out any) ([]byte, http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v4"+endpoint, nil)
	if err != nil {
		return nil, nil, errors.New("GitLab 请求地址无效")
	}
	request.Header.Set("PRIVATE-TOKEN", c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, nil, errors.New("无法连接 GitLab，请检查地址、网络和证书")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, nil, &discoveryHTTPError{response.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, nil, errors.New("GitLab 响应读取失败或超过 1 MiB 限制")
	}
	if out != nil && json.Unmarshal(data, out) != nil {
		return nil, nil, errors.New("GitLab 返回了无效的 JSON 数据")
	}
	return data, response.Header, nil
}
func discoveryReason(err error) string {
	var status *discoveryHTTPError
	if errors.As(err, &status) {
		switch status.Code {
		case 401:
			return "GitLab 凭据无效，请检查凭据环境变量"
		case 403:
			return "没有读取权限，请确认凭据具有仓库读取权限"
		case 404:
			return "未找到仓库或文件，请检查地址、引用及访问权限"
		}
	}
	return err.Error()
}
func (a *server) discover(w http.ResponseWriter, r *http.Request) {
	var input discoverRequest
	if !decode(w, r, &input) {
		return
	}
	output, err := a.discoverMeta(r.Context(), input)
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	respond(w, http.StatusOK, output)
}
func (a *server) discoverMeta(ctx context.Context, input discoverRequest) (discoverResponse, error) {
	output := discoverResponse{Repositories: []domain.Repository{}, Branches: map[string][]string{}, Warnings: []string{}}
	if input.MetaURL == "" {
		return output, errors.New("请填写 Meta-Repo 地址")
	}
	base, err := discoveryBase(input.GitLabURL, input.MetaURL)
	if err != nil {
		return output, err
	}
	metaPath, err := discoveryProjectPath(base, input.MetaURL)
	if err != nil {
		return output, fmt.Errorf("Meta-Repo 地址无效：%w", err)
	}
	if input.CredentialRef == "" {
		input.CredentialRef = "GITLAB_TOKEN"
	}
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(input.CredentialRef) {
		return output, errors.New("凭据引用必须是环境变量名称")
	}
	token, err := a.credential(input.CredentialRef)
	if err != nil || token == "" {
		return output, fmt.Errorf("凭据环境变量 %s 不可用，请先在服务端配置", input.CredentialRef)
	}
	if input.DefaultTargetBranch == "" {
		input.DefaultTargetBranch = "dev"
	}
	if !domain.ValidBranch(input.DefaultTargetBranch) {
		return output, errors.New("默认目标分支名称无效")
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	client := discoveryClient{base: base.String(), token: token, http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	lookup := func(projectPath string) (discoveryProject, error) {
		var project discoveryProject
		_, _, err := client.get(ctx, "/projects/"+url.PathEscape(projectPath), &project)
		if err != nil {
			return project, err
		}
		if project.ID <= 0 || project.Name == "" || project.HTTPURL == "" {
			return project, errors.New("GitLab 项目元数据缺少真实 ID、名称或仓库地址")
		}
		returned, err := discoveryProjectPath(base, project.HTTPURL)
		if err != nil || returned != projectPath {
			return project, errors.New("GitLab 返回的仓库地址与请求项目不一致")
		}
		return project, nil
	}
	meta, err := lookup(metaPath)
	if err != nil {
		return output, fmt.Errorf("读取 Meta-Repo 项目失败：%s", discoveryReason(err))
	}
	if input.MetaRef == "" {
		input.MetaRef = meta.DefaultBranch
	}
	if input.MetaRef == "" || strings.ContainsAny(input.MetaRef, "\x00\r\n") {
		return output, errors.New("Meta-Repo 没有默认分支，请填写明确的引用")
	}
	output.Name = meta.Name
	output.SuggestedID = discoveryID(path.Base(metaPath))
	output.GitLabURL = base.String()
	output.MetaURL = input.MetaURL
	output.MetaRef = input.MetaRef
	var manifest []byte
	for _, file := range []string{".meta.yaml", ".meta.yml", ".meta"} {
		manifest, _, err = client.get(ctx, "/projects/"+url.PathEscape(metaPath)+"/repository/files/"+url.PathEscape(file)+"/raw?ref="+url.QueryEscape(input.MetaRef), nil)
		if err == nil {
			break
		}
		var status *discoveryHTTPError
		if !errors.As(err, &status) || status.Code != 404 {
			return output, fmt.Errorf("读取 %s 失败：%s", file, discoveryReason(err))
		}
	}
	if err != nil {
		return output, errors.New("在指定 Meta-Repo 引用中未找到 .meta.yaml、.meta.yml 或 .meta")
	}
	entries, err := discoveryManifest(manifest)
	if err != nil {
		return output, err
	}
	used := map[string]bool{}
	for _, entry := range entries {
		if domain.IDPattern.MatchString(entry.Alias) {
			used[entry.Alias] = true
		}
	}
	included := []discoveryEntry{}
	paths := map[string]bool{}
	for _, entry := range entries {
		entry.ProjectPath, err = discoveryProjectPath(base, entry.URL)
		if err != nil {
			return output, fmt.Errorf("仓库 %s 地址无效：%w", entry.Alias, err)
		}
		if entry.ProjectPath == metaPath && entry.Path != "." {
			output.Warnings = append(output.Warnings, "已跳过 Meta-Repo 自身条目 "+entry.Alias+"；如需修改根仓库，请明确使用 path: .")
			continue
		}
		if entry.Path == "." && (entry.ProjectPath != metaPath || entry.URL != input.MetaURL) {
			return output, errors.New("根仓库 path: . 必须使用与 Meta-Repo 完全一致的地址")
		}
		if err = discoveryPath(entry.Path); err != nil {
			return output, fmt.Errorf("仓库 %s：%w", entry.Alias, err)
		}
		if paths[entry.Path] {
			return output, fmt.Errorf("仓库路径重复：%s", entry.Path)
		}
		for previous := range paths {
			if previous != "." && entry.Path != "." && (strings.HasPrefix(entry.Path, previous+"/") || strings.HasPrefix(previous, entry.Path+"/")) {
				return output, fmt.Errorf("仓库路径不能重叠：%s 与 %s", previous, entry.Path)
			}
		}
		paths[entry.Path] = true
		entry.ID = entry.Alias
		if !domain.IDPattern.MatchString(entry.ID) {
			entry.ID = discoveryID(entry.Alias)
			for salt := 1; used[entry.ID]; salt++ {
				entry.ID = discoveryID(entry.Alias + "#" + strconv.Itoa(salt))
			}
			used[entry.ID] = true
		}
		included = append(included, entry)
	}
	if len(included) == 0 {
		return output, errors.New("Meta 清单中没有可导入的子仓库")
	}
	dependencies, err := discoveryDependencies(included)
	if err != nil {
		return output, err
	}
	projectIDs := map[int64]bool{}
	for _, entry := range included {
		project := meta
		if entry.ProjectPath != metaPath {
			project, err = lookup(entry.ProjectPath)
			if err != nil {
				return output, fmt.Errorf("读取仓库 %s 失败：%s", entry.Alias, discoveryReason(err))
			}
		}
		if projectIDs[project.ID] {
			return output, fmt.Errorf("多个清单条目指向同一 GitLab 项目：%s", entry.Alias)
		}
		projectIDs[project.ID] = true
		repository := domain.Repository{ID: entry.ID, ProjectID: project.ID, URL: entry.URL, Path: entry.Path, TargetBranch: input.DefaultTargetBranch, DependsOn: dependencies[entry.ID], SensitivePaths: []string{}}
		output.Repositories = append(output.Repositories, repository)
		names, listErr := discoveryBranches(ctx, client, project.ID)
		output.Branches[entry.ID] = names
		if listErr != nil {
			output.Warnings = append(output.Warnings, fmt.Sprintf("仓库 %s 的分支列表不可用：%s；请手动确认目标分支", entry.Alias, discoveryReason(listErr)))
			continue
		}
		found := false
		for _, name := range names {
			found = found || name == input.DefaultTargetBranch
		}
		if !found {
			var branch struct {
				Name string `json:"name"`
			}
			_, _, branchErr := client.get(ctx, fmt.Sprintf("/projects/%d/repository/branches/%s", project.ID, url.PathEscape(input.DefaultTargetBranch)), &branch)
			if branchErr == nil && branch.Name == input.DefaultTargetBranch {
				output.Branches[entry.ID] = append(names, branch.Name)
			} else {
				var status *discoveryHTTPError
				if errors.As(branchErr, &status) && status.Code == 404 {
					output.Warnings = append(output.Warnings, fmt.Sprintf("仓库 %s 缺少目标分支 %s，请手动选择初始化引用；未自动设置 createFrom", entry.Alias, input.DefaultTargetBranch))
				} else {
					output.Warnings = append(output.Warnings, fmt.Sprintf("仓库 %s 无法确认目标分支 %s，请手动检查", entry.Alias, input.DefaultTargetBranch))
				}
			}
		}
	}
	return output, nil
}
func discoveryBase(raw, meta string) (*url.URL, error) {
	if raw == "" {
		u, err := url.Parse(meta)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, errors.New("SSH Meta-Repo 地址需要明确填写 GitLab HTTP(S) 地址")
		}
		raw = u.Scheme + "://" + u.Host
	}
	base, err := url.Parse(raw)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.RawPath != "" {
		return nil, errors.New("GitLab 地址必须是无账号、查询参数或片段的 HTTP(S) 地址")
	}
	base.Path = strings.TrimSuffix(strings.TrimRight(base.Path, "/"), "/api/v4")
	if base.Path != "" && path.Clean(base.Path) != base.Path {
		return nil, errors.New("GitLab 子路径无效")
	}
	return base, nil
}
func discoveryProjectPath(base *url.URL, raw string) (string, error) {
	var repoPath string
	sshTransport := false
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		sshTransport = true
		host, p, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !ok || !strings.EqualFold(host, base.Hostname()) || strings.ContainsAny(p, "?#") {
			return "", errors.New("SSH 仓库必须与配置的 GitLab 主机一致，且不能含查询参数或片段")
		}
		repoPath = "/" + strings.TrimPrefix(p, "/")
	} else {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("仓库 URL 格式无效或包含查询参数/片段")
		}
		if u.Scheme == "ssh" {
			sshTransport = true
			if u.User != nil && (u.User.Username() != "git" || u.User.String() != "git") {
				return "", errors.New("SSH 地址只允许 git 用户且不能内嵌凭据")
			}
			if !strings.EqualFold(u.Hostname(), base.Hostname()) || (u.Port() != "" && u.Port() != "22") {
				return "", errors.New("SSH 仓库主机或端口与配置不符")
			}
		} else if u.User != nil || u.Scheme != base.Scheme || !strings.EqualFold(u.Host, base.Host) {
			return "", errors.New("仓库必须与 GitLab 地址的协议、主机和端口完全一致，且不能内嵌凭据")
		}
		repoPath = u.Path
	}
	prefix := strings.TrimRight(base.Path, "/") + "/"
	// GitLab HTTP relative URL roots do not prefix SSH repository namespaces.
	if sshTransport {
		prefix = "/"
	}
	if !strings.HasPrefix(repoPath, prefix) {
		return "", errors.New("仓库 URL 不在配置的 GitLab 子路径下")
	}
	repoPath = strings.TrimSuffix(strings.TrimPrefix(repoPath, prefix), ".git")
	if repoPath == "" || strings.HasPrefix(repoPath, "/") || path.Clean(repoPath) != repoPath || strings.ContainsAny(repoPath, "\\\x00\r\n?#") || !strings.Contains(repoPath, "/") {
		return "", errors.New("仓库 URL 需要有效的命名空间和项目路径")
	}
	for _, part := range strings.Split(repoPath, "/") {
		if part == "." || part == ".." || part == "" {
			return "", errors.New("仓库路径包含非法段")
		}
	}
	return repoPath, nil
}
func discoveryID(alias string) string {
	if domain.IDPattern.MatchString(alias) {
		return alias
	}
	var b strings.Builder
	for _, r := range strings.ToLower(alias) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "repo"
	}
	if len(slug) > 60 {
		slug = strings.TrimRight(slug[:60], "-")
	}
	digest := sha256.Sum256([]byte(alias))
	return fmt.Sprintf("%s-%x", slug, digest[:6])
}
func discoveryPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, ".git") || strings.HasPrefix(p, ".worktrees") || strings.ContainsAny(p, "\\:\x00\r\n") {
		return errors.New("仓库路径必须是安全的相对路径")
	}
	return nil
}
func discoveryManifest(data []byte) ([]discoveryEntry, error) {
	var manifest struct {
		Projects     map[string]yaml.Node `yaml:"projects"`
		Repositories yaml.Node            `yaml:"repositories"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return nil, errors.New("Meta 清单不是有效的 YAML/JSON")
	}
	if manifest.Projects == nil {
		if manifest.Repositories.Kind != 0 {
			return nil, errors.New("检测到 repositories 清单；gitkb/meta 使用 projects 映射。请改为 projects: {仓库别名: {repo: 仓库地址, path: 相对路径}} 后提交到 Meta-Repo。")
		}
		return nil, errors.New("Meta 清单缺少 projects 映射，请使用 projects: {仓库别名: {repo: 仓库地址, path: 相对路径}}。")
	}
	if len(manifest.Projects) == 0 {
		return nil, errors.New("Meta 清单的 projects 为空，请至少添加一个仓库。")
	}
	if len(manifest.Projects) > 100 {
		return nil, fmt.Errorf("Meta 清单包含 %d 个仓库，超过当前 100 个的导入上限。", len(manifest.Projects))
	}
	aliases := []string{}
	for alias := range manifest.Projects {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	entries := []discoveryEntry{}
	for _, alias := range aliases {
		node := manifest.Projects[alias]
		entry := discoveryEntry{Alias: alias, Path: alias}
		if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
			entry.URL = node.Value
		} else if node.Kind == yaml.MappingNode {
			var extended struct {
				Repo      string   `yaml:"repo"`
				URL       string   `yaml:"url"`
				Path      string   `yaml:"path"`
				DependsOn []string `yaml:"depends_on"`
				Provides  []string `yaml:"provides"`
				Meta      bool     `yaml:"meta"`
			}
			if err := node.Decode(&extended); err != nil {
				return nil, fmt.Errorf("仓库 %s 的扩展配置无效，depends_on/provides 应为字符串数组", alias)
			}
			if extended.Meta {
				return nil, fmt.Errorf("仓库 %s 设置了 meta: true；暂不支持自动展开嵌套 Meta-Repo，请先在当前清单中明确列出子仓库", alias)
			}
			entry.URL = extended.Repo
			if entry.URL == "" {
				entry.URL = extended.URL
			}
			if extended.Path != "" {
				entry.Path = extended.Path
			}
			entry.DependsOn = extended.DependsOn
			entry.Provides = extended.Provides
		} else {
			return nil, fmt.Errorf("仓库 %s 必须是 URL 字符串或扩展对象", alias)
		}
		if entry.URL == "" {
			return nil, fmt.Errorf("仓库 %s 缺少 repo/URL", alias)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
func discoveryDependencies(entries []discoveryEntry) (map[string][]string, error) {
	aliases := map[string]string{}
	paths := map[string]string{}
	capabilities := map[string][]string{}
	for _, e := range entries {
		aliases[e.Alias] = e.ID
		paths[e.Path] = e.ID
		for _, provided := range e.Provides {
			capabilities[provided] = append(capabilities[provided], e.ID)
		}
	}
	result := map[string][]string{}
	for _, e := range entries {
		result[e.ID] = []string{}
		seen := map[string]bool{}
		for _, dep := range e.DependsOn {
			id := aliases[dep]
			if id == "" {
				id = paths[dep]
			}
			if id == "" {
				providers := capabilities[dep]
				if len(providers) != 1 {
					return nil, fmt.Errorf("仓库 %s 的依赖 %s 无法唯一解析，请使用明确的别名或路径", e.Alias, dep)
				}
				id = providers[0]
			}
			if id == e.ID {
				return nil, fmt.Errorf("仓库 %s 不能依赖自身", e.Alias)
			}
			if !seen[id] {
				result[e.ID] = append(result[e.ID], id)
				seen[id] = true
			}
		}
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return false
		}
		if done[id] {
			return true
		}
		visiting[id] = true
		for _, dep := range result[id] {
			if !visit(dep) {
				return false
			}
		}
		visiting[id] = false
		done[id] = true
		return true
	}
	for id := range result {
		if !visit(id) {
			return nil, errors.New("Meta 清单的仓库依赖存在循环")
		}
	}
	return result, nil
}
func discoveryBranches(ctx context.Context, c discoveryClient, id int64) ([]string, error) {
	names := []string{}
	for page := 1; page <= 3; page++ {
		var branches []struct {
			Name string `json:"name"`
		}
		_, headers, err := c.get(ctx, fmt.Sprintf("/projects/%d/repository/branches?per_page=100&page=%d", id, page), &branches)
		if err != nil {
			return names, err
		}
		for _, branch := range branches {
			if len(names) >= 300 {
				break
			}
			names = append(names, branch.Name)
		}
		if headers.Get("X-Next-Page") == "" && len(branches) < 100 {
			break
		}
	}
	return names, nil
}
