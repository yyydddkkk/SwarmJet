package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"swarmjet/internal/credentials"
	"swarmjet/internal/domain"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/secretbox"
)

// connectionStore is the persistence subset the console needs for saved GitLab
// credentials. *store.Store implements it; tests use an in-memory fake.
type connectionStore interface {
	SaveConnection(context.Context, domain.Connection, string) (domain.Connection, error)
	Connection(context.Context, string) (domain.Connection, error)
	Connections(context.Context) ([]domain.Connection, error)
	ConnectionSecrets(context.Context, string) (string, error)
	DeleteConnection(context.Context, string) error
}

type connectionRequest struct {
	Name       string `json:"name"`
	GitLabURL  string `json:"gitlabUrl"`
	Token      string `json:"token"`
	MergeToken string `json:"mergeToken"`
	Version    int    `json:"version"`
}

// gitlabProblem maps a GitLab client failure to a targeted, secret-free message.
func gitlabProblem(w http.ResponseWriter, err error) {
	if status, ok := gitlab.StatusCode(err); ok {
		switch status {
		case http.StatusUnauthorized:
			problem(w, http.StatusUnauthorized, "Token 无效或已过期，请在 GitLab 重新创建访问令牌。")
			return
		case http.StatusForbidden:
			problem(w, http.StatusForbidden, "Token 缺少所需权限，请确认具备 read_api 或 api 权限。")
			return
		case http.StatusNotFound:
			problem(w, http.StatusNotFound, "GitLab 未找到该接口，请确认填写的是实例根地址。")
			return
		}
	}
	problem(w, http.StatusBadGateway, "无法连接 GitLab，请检查地址、网络、代理和证书设置。")
}

func (a *server) connectionToken(id string, merge bool) (string, error) {
	if a.conns == nil || a.cfg.Box == nil {
		return "", errors.New("连接凭据存储未启用：请在部署中配置 SWARMJET_MASTER_KEY。")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sealed, err := a.conns.ConnectionSecrets(ctx, id)
	if err != nil {
		return "", errors.New("已保存的连接凭据不可用。")
	}
	plain, err := a.cfg.Box.Open(sealed)
	if err != nil {
		return "", err
	}
	var payload credentials.Payload
	if json.Unmarshal([]byte(plain), &payload) != nil || payload.Token == "" {
		return "", errors.New("已保存的连接凭据无效。")
	}
	if merge && payload.MergeToken != "" {
		return payload.MergeToken, nil
	}
	return payload.Token, nil
}

func (a *server) sealConnection(token, mergeToken string) (string, error) {
	if a.cfg.Box == nil {
		return "", errors.New("连接凭据存储未启用：请在部署中配置 SWARMJET_MASTER_KEY。")
	}
	body, err := json.Marshal(credentials.Payload{Token: token, MergeToken: mergeToken})
	if err != nil {
		return "", err
	}
	return a.cfg.Box.Seal(string(body))
}

// verifyConnection validates a token against the configured GitLab without
// persisting anything. The token is never echoed back.
func (a *server) verifyConnection(w http.ResponseWriter, r *http.Request) {
	var req connectionRequest
	if !decode(w, r, &req) {
		return
	}
	base, err := domain.NormalizeGitLabURL(req.GitLabURL)
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		problem(w, http.StatusBadRequest, "Access Token 不能为空。")
		return
	}
	if len(token) > 512 {
		problem(w, http.StatusBadRequest, "Access Token 过长。")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	user, err := gitlab.New(base, token).CurrentUser(ctx)
	if err != nil {
		gitlabProblem(w, err)
		return
	}
	mergeToken := strings.TrimSpace(req.MergeToken)
	if mergeToken != "" {
		if _, err = gitlab.New(base, mergeToken).CurrentUser(ctx); err != nil {
			problem(w, http.StatusBadRequest, "合并 Token 验证失败，请检查其权限，或留空以使用同一个 Token。")
			return
		}
	}
	respond(w, http.StatusOK, map[string]any{
		"ok":            true,
		"gitlabUrl":     base,
		"username":      user.Username,
		"name":          user.Name,
		"fingerprint":   secretbox.Fingerprint(token),
		"hasMergeToken": mergeToken != "",
	})
}

func (a *server) connections(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if a.conns == nil {
			respond(w, http.StatusOK, []domain.Connection{})
			return
		}
		list, err := a.conns.Connections(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, http.StatusOK, list)
	case http.MethodPost:
		var req connectionRequest
		if !decode(w, r, &req) {
			return
		}
		if req.Version != 0 {
			problem(w, http.StatusBadRequest, "new connection version must be zero")
			return
		}
		a.saveConnection(w, r, "", req)
	default:
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *server) connection(w http.ResponseWriter, r *http.Request, id string, tail []string) {
	if len(tail) == 1 && tail[0] == "repositories" {
		if r.Method != http.MethodGet {
			problem(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		a.connectionRepositories(w, r, id)
		return
	}
	if len(tail) != 0 {
		problem(w, http.StatusNotFound, "API route not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if a.conns == nil {
			problem(w, http.StatusNotFound, "not found")
			return
		}
		c, err := a.conns.Connection(r.Context(), id)
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, http.StatusOK, c)
	case http.MethodPut:
		var req connectionRequest
		if !decode(w, r, &req) {
			return
		}
		if req.Version < 1 {
			problem(w, http.StatusBadRequest, "current connection version is required")
			return
		}
		a.saveConnection(w, r, id, req)
	case http.MethodDelete:
		a.deleteConnection(w, r, id)
	default:
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *server) saveConnection(w http.ResponseWriter, r *http.Request, id string, req connectionRequest) {
	if a.conns == nil {
		problem(w, http.StatusServiceUnavailable, "连接存储未启用：请在部署中配置 SWARMJET_MASTER_KEY。")
		return
	}
	base, err := domain.NormalizeGitLabURL(req.GitLabURL)
	if err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var existing domain.Connection
	if id != "" {
		existing, err = a.conns.Connection(ctx, id)
		if err != nil {
			failure(w, err)
			return
		}
		if req.Version != existing.Version {
			problem(w, http.StatusConflict, "连接已被其他操作修改，请刷新后重试。")
			return
		}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = existing.Name
	}
	if name == "" {
		name = "GitLab " + base
	}
	if len(name) > 120 || strings.ContainsAny(name, "\x00\r\n") {
		problem(w, http.StatusBadRequest, "连接名称无效。")
		return
	}
	token := strings.TrimSpace(req.Token)
	mergeToken := strings.TrimSpace(req.MergeToken)
	sealed := ""
	username := existing.Username
	fingerprint := existing.Fingerprint
	hasMerge := existing.HasMergeToken
	if token != "" {
		if len(token) > 512 {
			problem(w, http.StatusBadRequest, "Access Token 过长。")
			return
		}
		user, verifyErr := gitlab.New(base, token).CurrentUser(ctx)
		if verifyErr != nil {
			gitlabProblem(w, verifyErr)
			return
		}
		username = user.Username
		fingerprint = secretbox.Fingerprint(token)
		sealed, err = a.sealConnection(token, mergeToken)
		if err != nil {
			problem(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		hasMerge = mergeToken != ""
	} else {
		if id == "" {
			problem(w, http.StatusBadRequest, "Access Token 不能为空。")
			return
		}
		// Keeping the stored token while changing the GitLab address would send
		// the old token to a different origin. Require a freshly verified token.
		if domain.GitLabIdentity(existing.GitLabURL) != domain.GitLabIdentity(base) {
			problem(w, http.StatusBadRequest, "更换 GitLab 地址必须重新提供并验证 Access Token。")
			return
		}
		sealed, err = a.conns.ConnectionSecrets(ctx, id)
		if err != nil {
			failure(w, err)
			return
		}
		if mergeToken != "" {
			if a.cfg.Box == nil {
				problem(w, http.StatusServiceUnavailable, "连接凭据存储未启用：请在部署中配置 SWARMJET_MASTER_KEY。")
				return
			}
			plain, openErr := a.cfg.Box.Open(sealed)
			if openErr != nil {
				failure(w, openErr)
				return
			}
			var payload credentials.Payload
			if json.Unmarshal([]byte(plain), &payload) != nil {
				problem(w, http.StatusInternalServerError, "连接凭据载荷无效。")
				return
			}
			payload.MergeToken = mergeToken
			body, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				failure(w, marshalErr)
				return
			}
			sealed, err = a.cfg.Box.Seal(string(body))
			if err != nil {
				failure(w, err)
				return
			}
			hasMerge = true
		}
	}
	if id == "" {
		id, err = newID()
		if err != nil {
			failure(w, err)
			return
		}
	}
	c := domain.Connection{ID: id, Name: name, GitLabURL: base, Username: username, Version: req.Version, HasMergeToken: hasMerge, Fingerprint: fingerprint, CreatedAt: existing.CreatedAt}
	saved, err := a.conns.SaveConnection(ctx, c, sealed)
	if err != nil {
		failure(w, err)
		return
	}
	respond(w, http.StatusOK, saved)
}

func (a *server) deleteConnection(w http.ResponseWriter, r *http.Request, id string) {
	if a.conns == nil {
		problem(w, http.StatusNotFound, "not found")
		return
	}
	if a.s != nil {
		projects, err := a.s.Projects(r.Context())
		if err != nil {
			failure(w, err)
			return
		}
		for _, p := range projects {
			if p.ConnectionID == id {
				problem(w, http.StatusConflict, "该连接正被项目 "+p.ID+" 使用，请先在项目中更换连接。")
				return
			}
		}
	}
	if err := a.conns.DeleteConnection(r.Context(), id); err != nil {
		failure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *server) connectionRepositories(w http.ResponseWriter, r *http.Request, id string) {
	if a.conns == nil {
		problem(w, http.StatusNotFound, "not found")
		return
	}
	c, err := a.conns.Connection(r.Context(), id)
	if err != nil {
		failure(w, err)
		return
	}
	token, err := a.connectionToken(id, false)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	perPage, _ := strconv.Atoi(r.URL.Query().Get("perPage"))
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	result, err := gitlab.New(c.GitLabURL, token).ListProjects(ctx, r.URL.Query().Get("search"), page, perPage)
	if err != nil {
		gitlabProblem(w, err)
		return
	}
	respond(w, http.StatusOK, result)
}
