package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"github.com/yyydddkkk/SwarmJet/internal/initflow"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// Initializer is the durable initialization state machine exposed to the
// console. It is implemented by initflow.Manager.
type Initializer interface {
	Ensure(context.Context, string) (domain.Initialization, error)
	Get(context.Context, string) (domain.Initialization, error)
	Retry(context.Context, string) error
	Cancel(context.Context, string) error
	Answer(context.Context, string, map[string]string) (domain.Initialization, error)
	// ReadyForTasks reports persisted verification readiness. It gates enabling a
	// managed project and accepting new tasks for it.
	ReadyForTasks(context.Context, string) bool
}

func initializationFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		problem(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		problem(w, http.StatusConflict, err.Error())
	default:
		problem(w, http.StatusBadRequest, err.Error())
	}
}

func (a *server) initialization(w http.ResponseWriter, r *http.Request, id string, tail []string) {
	if a.cfg.Initializer == nil {
		problem(w, http.StatusServiceUnavailable, "初始化服务未启用。")
		return
	}
	if len(tail) > 0 && tail[0] == "publication" {
		a.metaPublication(w, r, id, tail[1:])
		return
	}
	if len(tail) == 0 {
		switch r.Method {
		case http.MethodGet:
			init, err := a.cfg.Initializer.Get(r.Context(), id)
			if errors.Is(err, store.ErrNotFound) {
				respond(w, http.StatusOK, domain.Initialization{ProjectID: id, Status: "none", Stage: "none"})
				return
			}
			if err != nil {
				failure(w, err)
				return
			}
			respond(w, http.StatusOK, init)
		case http.MethodPost:
			init, err := a.cfg.Initializer.Ensure(r.Context(), id)
			if err != nil {
				initializationFailure(w, err)
				return
			}
			respond(w, http.StatusAccepted, init)
		default:
			problem(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if r.Method != http.MethodPost {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch tail[0] {
	case "retry":
		if err := a.cfg.Initializer.Retry(r.Context(), id); err != nil {
			initializationFailure(w, err)
			return
		}
		respond(w, http.StatusAccepted, map[string]bool{"accepted": true})
	case "cancel":
		if err := a.cfg.Initializer.Cancel(r.Context(), id); err != nil {
			initializationFailure(w, err)
			return
		}
		respond(w, http.StatusAccepted, map[string]bool{"accepted": true})
	case "answers":
		var req struct {
			Answers map[string]string `json:"answers"`
		}
		if !decode(w, r, &req) {
			return
		}
		if len(req.Answers) == 0 {
			problem(w, http.StatusBadRequest, "answers are required")
			return
		}
		init, err := a.cfg.Initializer.Answer(r.Context(), id, req.Answers)
		if err != nil {
			initializationFailure(w, err)
			return
		}
		respond(w, http.StatusAccepted, init)
	default:
		problem(w, http.StatusNotFound, "API route not found")
	}
}

func (a *server) metaPublication(w http.ResponseWriter, r *http.Request, id string, tail []string) {
	if len(tail) == 0 && r.Method == http.MethodPost {
		publisher, ok := a.cfg.Initializer.(interface {
			QueuePublication(context.Context, string, initflow.PublishRequest) (domain.Initialization, error)
		})
		if !ok {
			problem(w, 503, "发布服务未启用")
			return
		}
		var req initflow.PublishRequest
		if !decode(w, r, &req) {
			return
		}
		init, err := publisher.QueuePublication(r.Context(), id, req)
		if err != nil {
			initializationFailure(w, err)
			return
		}
		respond(w, http.StatusAccepted, init)
		return
	}
	if len(tail) != 1 || r.Method != http.MethodGet {
		problem(w, 404, "API route not found")
		return
	}
	p, err := a.s.Project(r.Context(), id)
	if err != nil {
		failure(w, err)
		return
	}
	if !p.ManagedMeta() {
		problem(w, 400, "只有受管 Meta 仓库可发布")
		return
	}
	if p.HostingProvider() == domain.ProviderGitHub {
		problem(w, 400, "GitHub 项目暂不支持远程 Meta 仓库发布")
		return
	}
	c, err := a.client(p)
	if err != nil {
		problem(w, 400, "GitLab 连接不可用")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	search := r.URL.Query().Get("search")
	if len(search) > 200 {
		problem(w, 400, "搜索内容过长")
		return
	}
	switch tail[0] {
	case "namespaces":
		rows, next, err := c.Namespaces(r.Context(), search, page)
		if err != nil {
			gitlabProblem(w, err)
			return
		}
		respond(w, 200, map[string]any{"items": rows, "page": page, "hasNext": next})
	case "repositories":
		rows, err := c.ListProjects(r.Context(), search, page, 50)
		if err != nil {
			gitlabProblem(w, err)
			return
		}
		respond(w, 200, rows)
	default:
		problem(w, 404, "API route not found")
	}
}
