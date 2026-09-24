package api

import (
	"errors"
	"net/http"

	"swarmjet/internal/store"
)

type developerOptions struct {
	PlatformImages bool `json:"platformImages"`
}

func (a *server) loadDeveloperOptions(r *http.Request) (developerOptions, error) {
	var options developerOptions
	if a.s == nil {
		return options, store.ErrNotFound
	}
	err := a.s.Setting(r.Context(), "developer_options", &options)
	if errors.Is(err, store.ErrNotFound) {
		return developerOptions{}, nil
	}
	return options, err
}

func (a *server) developerOptions(w http.ResponseWriter, r *http.Request) {
	if a.s == nil {
		problem(w, 503, "设置存储不可用")
		return
	}
	switch r.Method {
	case http.MethodGet:
		options, err := a.loadDeveloperOptions(r)
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, 200, options)
	case http.MethodPut:
		var options developerOptions
		if !decode(w, r, &options) {
			return
		}
		if err := a.s.SetSetting(r.Context(), "developer_options", options); err != nil {
			failure(w, err)
			return
		}
		_ = a.s.Event(r.Context(), "", "developer_options_updated", "管理员更新开发者选项")
		respond(w, 200, options)
	default:
		problem(w, 405, "method not allowed")
	}
}
