package api

import (
	"context"
	"errors"
	"net/http"

	"swarmjet/internal/domain"
	"swarmjet/internal/store"
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
