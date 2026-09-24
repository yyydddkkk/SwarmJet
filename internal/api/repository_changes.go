package api

import (
	"errors"
	"net/http"
	"strconv"

	"swarmjet/internal/repochange"
	"swarmjet/internal/store"
)

func (a *server) repositoryChanges(w http.ResponseWriter, r *http.Request, id string, tail []string) {
	manager := a.cfg.RepositoryChanges
	if manager == nil {
		problem(w, http.StatusServiceUnavailable, "仓库变更服务未启用")
		return
	}
	if len(tail) == 2 && tail[1] == "retry" && r.Method == http.MethodPost {
		change, err := manager.Retry(r.Context(), id, tail[0])
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				problem(w, http.StatusConflict, err.Error())
			} else {
				problem(w, http.StatusBadRequest, err.Error())
			}
			return
		}
		respond(w, http.StatusAccepted, change)
		return
	}
	if len(tail) != 0 {
		problem(w, http.StatusNotFound, "API route not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("available") == "true" {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			available, err := manager.Available(r.Context(), id, r.URL.Query().Get("search"), page)
			if err != nil {
				problem(w, http.StatusBadGateway, "无法读取 GitLab 仓库列表")
				return
			}
			respond(w, http.StatusOK, available)
			return
		}
		changes, err := manager.List(r.Context(), id)
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, http.StatusOK, changes)
	case http.MethodPost:
		var req repochange.AddRequest
		if !decode(w, r, &req) {
			return
		}
		change, err := manager.Start(r.Context(), id, req)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrNotFound):
				problem(w, http.StatusNotFound, "项目不存在")
			case errors.Is(err, store.ErrConflict):
				problem(w, http.StatusConflict, "该项目已有正在处理的仓库变更")
			default:
				problem(w, http.StatusBadRequest, err.Error())
			}
			return
		}
		respond(w, http.StatusAccepted, change)
	default:
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
