package initflow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"swarmjet/internal/domain"
	"swarmjet/internal/gitlab"
)

type PublishRequest struct {
	Mode              string `json:"mode"`
	NamespaceID       int64  `json:"namespaceId"`
	Path              string `json:"path"`
	ExistingProjectID int64  `json:"existingProjectId"`
}

// QueuePublication never creates a remote project in an HTTP request. The
// persisted intent is resumed by the worker and is independent of init status.
func (m *Manager) QueuePublication(ctx context.Context, id string, req PublishRequest) (domain.Initialization, error) {
	init, err := m.Get(ctx, id)
	if err != nil {
		return init, err
	}
	p, err := m.Store.Project(ctx, id)
	if err != nil {
		return init, err
	}
	if !p.ManagedMeta() || init.Status != domain.InitReady || init.MetaSHA == "" {
		return init, errors.New("请先完成本地 Meta 仓库初始化")
	}
	if init.Publication != nil {
		if init.Publication.Status == "published" || init.Publication.Status == "pending" {
			return init, nil
		}
		pub := *init.Publication
		// Retry the recorded target; a response loss must not create another repo.
		pub.Status = "pending"
		pub.Error = ""
		init.Publication = &pub
		return m.save(ctx, init, p)
	}
	c, err := m.client(p)
	if err != nil {
		return init, errors.New("GitLab 连接不可用")
	}
	pub := domain.MetaPublication{Status: "pending", Mode: req.Mode, Ref: p.MetaRef, SHA: init.MetaSHA, GitLabURL: p.GitLabURL, CredentialRef: p.CredentialReference()}
	nonce := make([]byte, 16)
	if _, err = rand.Read(nonce); err != nil {
		return init, err
	}
	pub.RequestID = hex.EncodeToString(nonce)
	switch req.Mode {
	case "create":
		if req.NamespaceID <= 0 || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`).MatchString(req.Path) {
			return init, errors.New("请选择群组或个人空间，并填写有效仓库名（字母、数字、横线、下划线）")
		}
		n, e := c.Namespace(ctx, req.NamespaceID)
		if e != nil {
			return init, errors.New("无法读取目标空间，请确认 Token 的 API 权限")
		}
		if n.ID != req.NamespaceID || n.FullPath == "" {
			return init, errors.New("GitLab 返回的目标空间无效")
		}
		pub.NamespaceID = n.ID
		pub.Path = n.FullPath + "/" + req.Path
		if _, e = c.MetaProject(ctx, pub.Path); e == nil {
			return init, errors.New("该路径已有仓库，请换一个名称或选择关联已有空仓库")
		} else if status, ok := gitlab.StatusCode(e); !ok || status != 404 {
			return init, errors.New("无法检查目标仓库，请确认 GitLab 地址和 Token 权限")
		}
	case "existing":
		if req.ExistingProjectID <= 0 {
			return init, errors.New("请选择已有空仓库")
		}
		remote, e := c.MetaProject(ctx, strconv.FormatInt(req.ExistingProjectID, 10))
		if e != nil {
			return init, errors.New("无法读取目标仓库")
		}
		if !remote.Empty || remote.Archived {
			return init, errors.New("只能关联未归档的空仓库，不会覆盖已有内容")
		}
		pub.RemoteID = remote.ID
		pub.Path = remote.PathWithNamespace
	default:
		return init, errors.New("请选择创建私有仓库或关联已有空仓库")
	}
	init.Publication = &pub
	return m.save(ctx, init, p)
}

func (m *Manager) publishMeta(parent context.Context, init domain.Initialization) (domain.Initialization, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	p, err := m.Store.Project(ctx, init.ProjectID)
	if err != nil {
		return init, err
	}
	pub := *init.Publication
	fail := func(message string) (domain.Initialization, error) {
		pub.Status = "failed"
		pub.Error = message
		init.Publication = &pub
		// Save failures separately; the local initialization remains complete.
		return m.save(parent, init, p)
	}
	if p.GitLabURL != pub.GitLabURL || p.CredentialReference() != pub.CredentialRef || p.MetaRef != pub.Ref {
		return fail("发布期间项目连接或分支已变更，请恢复原设置后重试")
	}
	c, err := m.client(p)
	if err != nil {
		return fail("GitLab 连接不可用，请检查保存的 Token")
	}
	lookup := pub.Path
	if pub.RemoteID > 0 {
		lookup = strconv.FormatInt(pub.RemoteID, 10)
	}
	remote, err := c.MetaProject(ctx, lookup)
	marker := "SwarmJet Meta repository publication " + pub.RequestID
	if status, ok := gitlab.StatusCode(err); ok && status == 404 && pub.Mode == "create" {
		leaf := pub.Path[strings.LastIndex(pub.Path, "/")+1:]
		remote, err = c.CreateMetaProject(ctx, pub.NamespaceID, leaf, marker)
	}
	if err != nil {
		return fail("GitLab 创建或读取仓库失败，请检查 API 权限、目标空间的创建权限和网络，然后重试")
	}
	if pub.Mode == "create" && (remote.Description != marker || remote.Visibility != "private") {
		return fail("目标路径已被其他仓库占用，未推送任何内容")
	}
	base, _ := url.Parse(p.GitLabURL)
	target, e := url.Parse(remote.HTTPURLToRepo)
	if e != nil || base == nil || target.User != nil || target.RawQuery != "" || target.Fragment != "" || target.Scheme != base.Scheme || target.Host != base.Host {
		return fail("目标仓库地址与 GitLab 连接来源不一致，已拒绝发送凭证")
	}
	token, err := m.projectCredential(p)
	if err != nil || token == "" {
		return fail("无法读取 GitLab 凭证")
	}
	env := m.gitEnv(p)
	env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.followRedirects", "GIT_CONFIG_VALUE_0=false")
	root, err := m.Managed.Root(p.ID)
	if err != nil {
		return fail("本地 Meta 仓库不可用")
	}
	unlock, err := m.Managed.Lock(ctx, p.ID)
	if err != nil {
		return fail("无法锁定本地 Meta 仓库")
	}
	defer unlock()
	if _, err = m.runGit(ctx, root, env, "cat-file", "-e", pub.SHA+"^{commit}"); err != nil {
		return fail("初始化的固定 Meta 提交不可用")
	}
	refs, err := m.runGit(ctx, root, env, "ls-remote", "--refs", remote.HTTPURLToRepo)
	if err != nil {
		return fail("无法读取远程 Git 引用，请检查仓库读写权限")
	}
	branch := "refs/heads/" + pub.Ref
	found := false
	for _, line := range strings.Split(strings.TrimSpace(refs), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) == 2 && fields[0] == pub.SHA && fields[1] == branch {
			found = true
			continue
		}
	}
	if !found {
		if strings.TrimSpace(refs) != "" {
			return fail("目标仓库不再为空，未覆盖远程内容")
		}
		// The empty expected value makes creation conditional on the branch still
		// being absent. Never force-update an existing branch. Skip repository CI.
		if _, err = m.runGit(ctx, root, env, "push", "-o", "ci.skip", "--force-with-lease="+branch+":", remote.HTTPURLToRepo, pub.SHA+":"+branch); err != nil {
			return fail("推送失败：请确认 write_repository 权限和分支保护设置。目标记录已保留，可重试")
		}
	}
	web, webErr := url.Parse(remote.WebURL)
	if webErr != nil || web.Scheme != base.Scheme || web.Host != base.Host || web.User != nil {
		return fail("GitLab 返回的仓库网页地址无效；推送已保留，可重试确认")
	}
	pub.RemoteID = remote.ID
	pub.CloneURL = remote.HTTPURLToRepo
	pub.URL = remote.WebURL
	pub.Status = "published"
	pub.Error = ""
	init.Publication = &pub
	return m.save(parent, init, p)
}
