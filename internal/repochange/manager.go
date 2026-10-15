package repochange

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/gitlab"
	"github.com/yyydddkkk/SwarmJet/internal/managedmeta"
)

type Store interface {
	UpgradeMaintenance(context.Context) (bool, error)
	Project(context.Context, string) (domain.Project, error)
	SaveProject(context.Context, domain.Project) (domain.Project, error)
	SaveRepositoryChange(context.Context, domain.RepositoryChange) error
	RepositoryChange(context.Context, string) (domain.RepositoryChange, error)
	RepositoryChanges(context.Context, string) ([]domain.RepositoryChange, error)
	Initialization(context.Context, string) (domain.Initialization, error)
	SaveInitialization(context.Context, domain.Initialization) error
	Lease(context.Context, string, string, time.Duration) (bool, error)
	ReleaseLeases(context.Context, string) error
}

type Manager struct {
	Store         Store
	Managed       *managedmeta.Manager
	CredentialFor func(domain.Project) (string, error)
	AskPass       string
	Logger        *slog.Logger
}

type AddRequest struct {
	GitLabProjectID int64    `json:"gitlabProjectId"`
	Alias           string   `json:"alias"`
	Path            string   `json:"path"`
	TargetBranch    string   `json:"targetBranch"`
	CreateFrom      string   `json:"createFrom"`
	DependsOn       []string `json:"dependsOn"`
}

func (m *Manager) Available(ctx context.Context, projectID, search string, page int) (gitlab.ProjectPage, error) {
	p, err := m.Store.Project(ctx, projectID)
	if err != nil {
		return gitlab.ProjectPage{}, err
	}
	c, err := m.client(p)
	if err != nil {
		return gitlab.ProjectPage{}, err
	}
	return c.ListProjects(ctx, search, page, 50)
}

func (m *Manager) client(p domain.Project) (*gitlab.Client, error) {
	if m.CredentialFor == nil {
		return nil, errors.New("GitLab 凭据解析器未配置")
	}
	token, err := m.CredentialFor(p)
	if err != nil || token == "" {
		return nil, errors.New("GitLab 连接不可用")
	}
	return gitlab.New(p.GitLabURL, token), nil
}

func (m *Manager) Start(ctx context.Context, projectID string, req AddRequest) (domain.RepositoryChange, error) {
	p, err := m.Store.Project(ctx, projectID)
	if err != nil {
		return domain.RepositoryChange{}, err
	}
	if p.Version < 1 || req.GitLabProjectID <= 0 {
		return domain.RepositoryChange{}, errors.New("请选择有效的 GitLab 仓库")
	}
	if p.DirectRepo() {
		return domain.RepositoryChange{}, errors.New("单仓库项目不支持增加子仓库")
	}
	if p.ManagedMeta() {
		init, initErr := m.Store.Initialization(ctx, "init-"+p.ID)
		if initErr != nil || init.Status != domain.InitReady || !init.ReadyForTasks {
			return domain.RepositoryChange{}, errors.New("请先完成受管 Meta 初始化")
		}
		if init.Publication != nil && init.Publication.Status != "published" {
			return domain.RepositoryChange{}, errors.New("Meta 发布仍在处理或等待重试，请完成发布后再增加仓库")
		}
		if init.Publication != nil && !publicationMatches(p, init.Publication) {
			return domain.RepositoryChange{}, errors.New("Meta 发布记录与项目连接或分支不一致")
		}
	}
	c, err := m.client(p)
	if err != nil {
		return domain.RepositoryChange{}, err
	}
	info, err := c.MetaProjectByID(ctx, req.GitLabProjectID)
	if err != nil || info.ID != req.GitLabProjectID || info.Archived || info.HTTPURLToRepo == "" {
		return domain.RepositoryChange{}, errors.New("无法读取所选 GitLab 仓库，或该仓库已归档")
	}
	base, baseErr := url.Parse(p.GitLabURL)
	remote, remoteErr := url.Parse(info.HTTPURLToRepo)
	if baseErr != nil || remoteErr != nil || base.Scheme != remote.Scheme || base.Host != remote.Host || remote.User != nil || remote.RawQuery != "" || remote.Fragment != "" {
		return domain.RepositoryChange{}, errors.New("所选仓库地址与项目 GitLab 连接不一致")
	}
	if req.Alias == "" {
		req.Alias = path.Base(strings.TrimSuffix(remote.Path, ".git"))
	}
	if req.Path == "" {
		req.Path = req.Alias
	}
	if req.TargetBranch == "" {
		req.TargetBranch = info.DefaultBranch
	}
	repo := domain.Repository{ID: req.Alias, ProjectID: info.ID, URL: info.HTTPURLToRepo, Path: req.Path, TargetBranch: req.TargetBranch, CreateFrom: req.CreateFrom, DependsOn: req.DependsOn}
	for _, existing := range p.Repositories {
		if existing.ProjectID == repo.ProjectID || existing.ID == repo.ID || existing.Path == repo.Path {
			return domain.RepositoryChange{}, errors.New("该仓库、标识或路径已在项目中")
		}
	}
	candidate := p
	candidate.Repositories = append(append([]domain.Repository(nil), p.Repositories...), repo)
	if err := domain.ValidateProjectForSave(candidate); err != nil {
		return domain.RepositoryChange{}, err
	}
	if _, err := c.GetBranch(ctx, repo.ProjectID, repo.TargetBranch); err != nil {
		if code, ok := gitlab.StatusCode(err); !ok || code != 404 || repo.CreateFrom == "" {
			return domain.RepositoryChange{}, errors.New("目标分支不可读；不存在时请指定创建起点")
		}
		if _, err = c.CommitSHA(ctx, repo.ProjectID, repo.CreateFrom); err != nil {
			return domain.RepositoryChange{}, errors.New("创建起点分支或提交不可读")
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return domain.RepositoryChange{}, err
	}
	now := time.Now().UTC()
	change := domain.RepositoryChange{ID: hex.EncodeToString(random[:]), ProjectID: projectID, Repository: repo, Status: "pending", Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := m.Store.SaveRepositoryChange(ctx, change); err != nil {
		return domain.RepositoryChange{}, err
	}
	return change, nil
}

func (m *Manager) List(ctx context.Context, projectID string) ([]domain.RepositoryChange, error) {
	return m.Store.RepositoryChanges(ctx, projectID)
}

func (m *Manager) Retry(ctx context.Context, projectID, id string) (domain.RepositoryChange, error) {
	change, err := m.Store.RepositoryChange(ctx, id)
	if err != nil {
		return change, err
	}
	if change.ProjectID != projectID || change.Status != "blocked" {
		return change, errors.New("只有该项目已阻断的新增请求可以重试")
	}
	change.Status = "pending"
	change.Attempts = 0
	change.Error = ""
	if err = m.save(ctx, &change); err != nil {
		return change, err
	}
	return change, nil
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		maintenance, checkErr := m.Store.UpgradeMaintenance(ctx)
		if checkErr != nil || maintenance {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				continue
			}
		}
		changes, err := m.Store.RepositoryChanges(ctx, "")
		if err != nil {
			m.log("list repository changes", err)
		} else {
			for _, change := range changes {
				if change.Error != "" && time.Since(change.UpdatedAt) < 30*time.Second {
					continue
				}
				m.advanceWithLease(ctx, change)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) advanceWithLease(ctx context.Context, change domain.RepositoryChange) {
	owner := "repository-change-" + change.ID
	ok, err := m.Store.Lease(ctx, owner, owner, 2*time.Minute)
	if err != nil || !ok {
		return
	}
	defer m.Store.ReleaseLeases(context.WithoutCancel(ctx), owner)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-workCtx.Done():
				return
			case <-ticker.C:
				if renewed, e := m.Store.Lease(workCtx, owner, owner, 2*time.Minute); e != nil || !renewed {
					cancel()
					return
				}
			}
		}
	}()
	current, err := m.Store.RepositoryChange(workCtx, change.ID)
	if err != nil || (current.Status != "pending" && current.Status != "waiting") {
		return
	}
	if err := m.advance(workCtx, &current); err != nil {
		if workCtx.Err() != nil {
			return
		}
		current.Error = err.Error()
		current.Attempts++
		if current.Status == "pending" && current.Attempts >= 3 {
			current.Status = "blocked"
		}
		current.UpdatedAt = time.Now().UTC()
		current.Version++
		if saveErr := m.Store.SaveRepositoryChange(workCtx, current); saveErr != nil {
			m.log("save repository change error", saveErr)
		}
	}
}

func (m *Manager) advance(ctx context.Context, change *domain.RepositoryChange) error {
	p, err := m.Store.Project(ctx, change.ProjectID)
	if err != nil {
		return err
	}
	if change.Status == "waiting" {
		return m.waitForMR(ctx, change, p)
	}
	for _, existing := range p.Repositories {
		if existing.ProjectID == change.Repository.ProjectID {
			if existing.ID != change.Repository.ID || existing.Path != change.Repository.Path || existing.URL != change.Repository.URL {
				return m.block(ctx, change, "项目中已有同一个 GitLab 仓库，但标识、路径或地址不同")
			}
			return m.finish(ctx, change)
		}
	}
	if p.ManagedMeta() {
		init, err := m.Store.Initialization(ctx, "init-"+p.ID)
		if err != nil {
			return err
		}
		if init.Publication == nil || init.Publication.Status != "published" {
			return m.applyLocal(ctx, change, p)
		}
		if !publicationMatches(p, init.Publication) {
			return m.block(ctx, change, "Meta 发布记录与项目连接或分支不一致")
		}
		return m.prepareRemote(ctx, change, p, init.Publication.RemoteID)
	}
	c, err := m.client(p)
	if err != nil {
		return err
	}
	metaPath, err := importedProjectPath(p)
	if err != nil {
		return err
	}
	meta, err := c.MetaProject(ctx, metaPath)
	if err != nil {
		return errors.New("无法读取 Meta-Repo 项目")
	}
	return m.prepareRemote(ctx, change, p, meta.ID)
}

func publicationMatches(p domain.Project, pub *domain.MetaPublication) bool {
	return pub != nil && pub.GitLabURL == p.GitLabURL && pub.CredentialRef == p.CredentialReference() && pub.Ref == p.MetaRef
}

func importedProjectPath(p domain.Project) (string, error) {
	base, err := url.Parse(p.GitLabURL)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(p.MetaURL, "git@") {
		hostPath := strings.TrimPrefix(p.MetaURL, "git@")
		host, name, ok := strings.Cut(hostPath, ":")
		if !ok || host != base.Hostname() {
			return "", errors.New("Meta-Repo 地址与 GitLab 连接不一致")
		}
		return strings.TrimSuffix(name, ".git"), nil
	}
	remote, err := url.Parse(p.MetaURL)
	if err != nil || remote.Hostname() != base.Hostname() || remote.User != nil {
		return "", errors.New("Meta-Repo 地址与 GitLab 连接不一致")
	}
	prefix := strings.TrimSuffix(base.Path, "/api/v4")
	name := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(remote.Path, prefix), ".git"), "/")
	if name == "" {
		return "", errors.New("Meta-Repo 路径无效")
	}
	return name, nil
}

func (m *Manager) save(ctx context.Context, change *domain.RepositoryChange) error {
	change.Error = ""
	change.UpdatedAt = time.Now().UTC()
	change.Version++
	return m.Store.SaveRepositoryChange(ctx, *change)
}

func (m *Manager) finish(ctx context.Context, change *domain.RepositoryChange) error {
	change.Status = "applied"
	change.DocsStale = true
	return m.save(ctx, change)
}

func (m *Manager) applyProject(ctx context.Context, change *domain.RepositoryChange, p domain.Project) error {
	for _, existing := range p.Repositories {
		if existing.ProjectID == change.Repository.ProjectID {
			if existing.ID != change.Repository.ID || existing.Path != change.Repository.Path || existing.URL != change.Repository.URL {
				return m.block(ctx, change, "项目中已有同一个 GitLab 仓库，但标识、路径或地址不同")
			}
			return m.finish(ctx, change)
		}
	}
	c, err := m.client(p)
	if err != nil {
		return err
	}
	if _, err = c.EnsureTarget(ctx, change.Repository); err != nil {
		return fmt.Errorf("目标分支尚未就绪：%w", err)
	}
	p.Repositories = append(p.Repositories, change.Repository)
	if _, err = m.Store.SaveProject(ctx, p); err != nil {
		return err
	}
	return m.finish(ctx, change)
}

func (m *Manager) log(message string, err error) {
	if m.Logger != nil {
		m.Logger.Error(message, "error", err)
	}
}

func sourceBranch(change domain.RepositoryChange) string {
	return "swarmjet/add-repo-" + change.ID
}
