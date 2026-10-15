package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
	"github.com/yyydddkkk/SwarmJet/internal/gitlab"
)

func (a *server) githubInstallations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.cfg.GitHubApp == nil {
		problem(w, http.StatusServiceUnavailable, "GitHub App 未配置。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	installations, err := a.cfg.GitHubApp.ListInstallations(ctx)
	if err != nil {
		problem(w, http.StatusBadGateway, "无法读取 GitHub App 安装。")
		return
	}
	respond(w, http.StatusOK, installations)
}

func (a *server) githubInstallationConnection(w http.ResponseWriter, r *http.Request, rawID string) {
	if r.Method != http.MethodPost {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if a.cfg.GitHubApp == nil || a.conns == nil {
		problem(w, http.StatusServiceUnavailable, "GitHub App 未配置。")
		return
	}
	installationID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || installationID <= 0 {
		problem(w, http.StatusBadRequest, "无效的安装 ID。")
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	installation, err := a.cfg.GitHubApp.GetInstallation(r.Context(), installationID)
	if err != nil {
		problem(w, http.StatusBadRequest, "GitHub App 安装不可用。")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = installation.Account.Login + "@github.com"
	}
	id, err := newID()
	if err != nil {
		failure(w, err)
		return
	}
	conn := domain.Connection{ID: id, Name: name, Provider: domain.ProviderGitHub, GitHubInstallationID: installationID, Username: installation.Account.Login}
	conn, err = a.conns.SaveConnection(r.Context(), conn, "")
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, http.StatusCreated, conn)
}

func (a *server) githubClient(c domain.Connection, repositoryIDs []int64) (*github.Client, error) {
	if a.cfg.GitHubApp == nil || c.HostingProvider() != domain.ProviderGitHub || c.GitHubInstallationID <= 0 {
		return nil, fmt.Errorf("GitHub App connection unavailable")
	}
	return github.New(func(ctx context.Context) (string, error) {
		if len(repositoryIDs) == 0 {
			token, err := a.cfg.GitHubApp.DiscoveryToken(ctx, c.GitHubInstallationID)
			return token.Token, err
		}
		token, err := a.cfg.GitHubApp.InstallationToken(ctx, c.GitHubInstallationID, repositoryIDs)
		return token.Token, err
	}), nil
}

func (a *server) validateGitHubProject(ctx context.Context, p domain.Project, c domain.Connection) error {
	if p.HostingProvider() != domain.ProviderGitHub || c.HostingProvider() != domain.ProviderGitHub {
		return fmt.Errorf("GitHub 项目需要 GitHub App 连接")
	}
	if p.DirectRepo() && len(p.Repositories) != 1 {
		return fmt.Errorf("GitHub 单仓库项目必须恰好选择一个仓库")
	}
	client, err := a.githubClient(c, nil)
	if err != nil {
		return err
	}
	available, err := client.ListRepositories(ctx)
	if err != nil {
		return fmt.Errorf("无法读取已授权的 GitHub 仓库")
	}
	byID := make(map[int64]github.Repository, len(available))
	for _, repo := range available {
		byID[repo.ID] = repo
	}
	for _, repo := range p.Repositories {
		found, ok := byID[repo.ProjectID]
		if !ok || found.Archived || found.CloneURL != repo.URL {
			return fmt.Errorf("仓库 %s 不在所选 GitHub App 安装中，或地址已变化", repo.ID)
		}
	}
	return nil
}

func (a *server) githubConnectionRepositories(w http.ResponseWriter, r *http.Request, c domain.Connection) {
	client, err := a.githubClient(c, nil)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	all, err := client.ListRepositories(ctx)
	if err != nil {
		problem(w, http.StatusBadGateway, "无法读取 GitHub 仓库。")
		return
	}
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
	filtered := make([]gitlab.ProjectInfo, 0, len(all))
	for _, repo := range all {
		if search != "" && !strings.Contains(strings.ToLower(repo.FullName), search) {
			continue
		}
		filtered = append(filtered, gitlab.ProjectInfo{ID: repo.ID, Name: repo.Name, PathWithNamespace: repo.FullName, DefaultBranch: repo.DefaultBranch, HTTPURLToRepo: repo.CloneURL, WebURL: repo.HTMLURL, Archived: repo.Archived, Description: repo.Description})
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("perPage"))
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 100 {
		perPage = 20
	}
	start := (page - 1) * perPage
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + perPage
	if end > len(filtered) {
		end = len(filtered)
	}
	respond(w, http.StatusOK, gitlab.ProjectPage{Projects: filtered[start:end], Page: page, PerPage: perPage, HasNext: end < len(filtered), HasPrev: page > 1})
}

func (a *server) githubConnectionBranches(w http.ResponseWriter, r *http.Request, c domain.Connection, repositoryID int64) {
	client, err := a.githubClient(c, []int64{repositoryID})
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	branches, err := client.ListBranches(ctx, repositoryID)
	if err != nil {
		problem(w, http.StatusBadGateway, "无法读取 GitHub 分支。")
		return
	}
	names := make([]string, 0, len(branches))
	for _, branch := range branches {
		names = append(names, branch.Name)
	}
	respond(w, http.StatusOK, names)
}
