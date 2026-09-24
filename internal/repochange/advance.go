package repochange

import (
	"context"
	"errors"
	"fmt"
	"time"

	"swarmjet/internal/domain"
	"swarmjet/internal/gitlab"
)

func (m *Manager) applyLocal(ctx context.Context, change *domain.RepositoryChange, p domain.Project) error {
	if m.Managed == nil {
		return errors.New("受管 Meta-Repo 不可用")
	}
	unlock, err := m.Managed.Lock(ctx, p.ID)
	if err != nil {
		return err
	}
	defer unlock()
	body, err := m.Managed.ReadFile(ctx, p.ID, p.MetaRef, ".meta.yaml")
	if err != nil {
		return errors.New("受管 Meta 清单不可读")
	}
	updated, changed, err := addManifestEntry(body, change.Repository, p.Repositories)
	if err != nil {
		return err
	}
	if changed {
		if _, err = m.Managed.Commit(ctx, p.ID, p.MetaRef, "登记子仓库 "+change.Repository.ID, map[string]string{".meta.yaml": string(updated)}); err != nil {
			return err
		}
	}
	sha, err := m.Managed.Ensure(ctx, p.ID, p.MetaRef, nil)
	if err != nil {
		return err
	}
	init, err := m.Store.Initialization(ctx, "init-"+p.ID)
	if err != nil {
		return err
	}
	if init.MetaSHA != sha {
		init.MetaSHA = sha
		init.Version++
		if err = m.Store.SaveInitialization(ctx, init); err != nil {
			return err
		}
	}
	return m.applyProject(ctx, change, p)
}

func (m *Manager) prepareRemote(ctx context.Context, change *domain.RepositoryChange, p domain.Project, metaID int64) error {
	if metaID <= 0 {
		return errors.New("远端 Meta-Repo 项目 ID 不可用")
	}
	c, err := m.client(p)
	if err != nil {
		return err
	}
	branch, err := c.GetBranch(ctx, metaID, p.MetaRef)
	if err != nil || branch.Commit.ID == "" {
		return errors.New("Meta 分支不可读")
	}
	name, body, err := remoteManifest(ctx, c, metaID, branch.Commit.ID)
	if err != nil {
		return err
	}
	updated, changed, err := addManifestEntry(body, change.Repository, p.Repositories)
	if err != nil {
		return err
	}
	if !changed {
		if p.ManagedMeta() {
			if err := m.syncPublished(ctx, p); err != nil {
				return err
			}
		}
		return m.applyProject(ctx, change, p)
	}
	source := sourceBranch(*change)
	if existing, lookupErr := c.GetBranch(ctx, metaID, source); lookupErr == nil {
		if existing.Commit.ID == "" {
			return errors.New("Meta 变更分支缺少提交")
		}
		_, sourceBody, readErr := remoteManifest(ctx, c, metaID, source)
		if readErr != nil {
			return readErr
		}
		if _, stillMissing, checkErr := addManifestEntry(sourceBody, change.Repository, p.Repositories); checkErr != nil || stillMissing {
			return errors.New("已有 Meta 变更分支内容与新增请求不一致")
		}
	} else if code, ok := gitlab.StatusCode(lookupErr); ok && code == 404 {
		if _, err = c.CreateFileCommit(ctx, metaID, source, branch.Commit.ID, name, string(updated), "登记子仓库 "+change.Repository.ID); err != nil {
			return fmt.Errorf("无法提交 Meta 清单变更，请检查写入权限：%w", err)
		}
	} else {
		return errors.New("无法检查 Meta 变更分支")
	}
	mr, err := c.FindMR(ctx, metaID, source, p.MetaRef)
	if err != nil {
		return errors.New("无法检查 Meta 变更 MR")
	}
	if mr == nil {
		created, createErr := c.CreateMR(ctx, metaID, source, p.MetaRef, "登记子仓库 "+change.Repository.ID,
			"由 SwarmJet 项目设置发起。合并后会自动登记仓库并按项目现有规则接单。\n\n仓库："+change.Repository.URL+"\n路径："+change.Repository.Path)
		if createErr != nil {
			return fmt.Errorf("无法创建 Meta 变更 MR，请检查 API 权限：%w", createErr)
		}
		mr = &created
	}
	if mr.IID <= 0 || mr.URL == "" {
		return errors.New("GitLab 返回的 Meta MR 信息不完整")
	}
	change.Status = "waiting"
	change.MetaProjectID = metaID
	change.MetaFile = name
	change.SourceBranch = source
	change.MergeRequestID = mr.IID
	change.MergeRequestURL = mr.URL
	return m.save(ctx, change)
}

func remoteManifest(ctx context.Context, c *gitlab.Client, projectID int64, ref string) (string, []byte, error) {
	for _, name := range []string{".meta.yaml", ".meta.yml", ".meta"} {
		body, err := c.RepositoryFile(ctx, projectID, name, ref)
		if err == nil {
			return name, body, nil
		}
		if code, ok := gitlab.StatusCode(err); !ok || code != 404 {
			return "", nil, errors.New("无法读取 Meta 清单")
		}
	}
	return "", nil, errors.New("Meta 分支缺少清单文件")
}

func (m *Manager) waitForMR(ctx context.Context, change *domain.RepositoryChange, p domain.Project) error {
	c, err := m.client(p)
	if err != nil {
		return err
	}
	mr, err := c.GetMR(ctx, change.MetaProjectID, change.MergeRequestID)
	if err != nil {
		return errors.New("无法读取 Meta 变更 MR")
	}
	if mr.SourceBranch != change.SourceBranch || mr.TargetBranch != p.MetaRef {
		return m.block(ctx, change, "Meta MR 来源或目标已改变")
	}
	switch mr.State {
	case "opened":
		return nil
	case "merged":
		branch, err := c.GetBranch(ctx, change.MetaProjectID, p.MetaRef)
		if err != nil || branch.Commit.ID == "" {
			return errors.New("合并后的 Meta 分支不可读")
		}
		_, body, err := remoteManifest(ctx, c, change.MetaProjectID, branch.Commit.ID)
		if err != nil {
			return err
		}
		if _, missing, err := addManifestEntry(body, change.Repository, p.Repositories); err != nil || missing {
			return m.block(ctx, change, "MR 已合并，但目标 Meta 清单未包含预期仓库")
		}
		if p.ManagedMeta() {
			if err := m.syncPublished(ctx, p); err != nil {
				return err
			}
		}
		return m.applyProject(ctx, change, p)
	default:
		change.Status = "closed"
		change.Error = "Meta 变更 MR 已关闭，仓库未加入项目"
		change.UpdatedAt = time.Now().UTC()
		change.Version++
		return m.Store.SaveRepositoryChange(ctx, *change)
	}
}

func (m *Manager) syncPublished(ctx context.Context, p domain.Project) error {
	if m.Managed == nil {
		return errors.New("受管 Meta-Repo 不可用")
	}
	init, err := m.Store.Initialization(ctx, "init-"+p.ID)
	if err != nil || init.Publication == nil || init.Publication.Status != "published" || !publicationMatches(p, init.Publication) {
		return errors.New("受管 Meta 发布记录不可用")
	}
	token, err := m.CredentialFor(p)
	if err != nil || token == "" {
		return errors.New("GitLab 连接不可用")
	}
	return m.Managed.SyncRemote(ctx, p.ID, p.MetaRef, init.Publication.CloneURL, map[string]string{"GITLAB_TOKEN": token, "GIT_ASKPASS": m.AskPass})
}

func (m *Manager) block(ctx context.Context, change *domain.RepositoryChange, reason string) error {
	change.Status = "blocked"
	change.Error = reason
	change.UpdatedAt = time.Now().UTC()
	change.Version++
	return m.Store.SaveRepositoryChange(ctx, *change)
}
