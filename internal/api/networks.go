package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"swarmjet/internal/domain"
	"swarmjet/internal/store"
)

func (a *server) networks(w http.ResponseWriter, r *http.Request) {
	if a.s == nil {
		problem(w, 503, "配置存储未就绪")
		return
	}
	if r.URL.Path == "/api/networks" && r.Method == "DELETE" {
		id := r.URL.Query().Get("id")
		version, e := strconv.Atoi(r.URL.Query().Get("version"))
		if !domain.IDPattern.MatchString(id) || version < 1 || e != nil {
			problem(w, 400, "网络配置 ID 或版本无效")
			return
		}
		if e = a.s.DeleteNetwork(r.Context(), id, version); e != nil {
			failure(w, e)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.URL.Path == "/api/networks/default" {
		if r.Method != "PUT" {
			problem(w, 405, "method not allowed")
			return
		}
		var req struct {
			ID string `json:"id"`
		}
		if !decode(w, r, &req) {
			return
		}
		if req.ID != "" {
			p, _, e := a.s.NetworkProfile(r.Context(), domain.NetworkVersion{ID: req.ID})
			if e != nil || p.Revoked {
				problem(w, 400, "网络配置不可用")
				return
			}
		}
		if e := a.s.SetSetting(r.Context(), "default_network", req.ID); e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, req)
		return
	}
	if r.Method == "GET" {
		items, e := a.s.NetworkProfiles(r.Context())
		if e != nil {
			failure(w, e)
			return
		}
		var id string
		e = a.s.Setting(r.Context(), "default_network", &id)
		if e != nil && !errors.Is(e, store.ErrNotFound) {
			failure(w, e)
			return
		}
		respond(w, 200, map[string]any{"profiles": items, "defaultId": id})
		return
	}
	if r.Method != "PUT" {
		problem(w, 405, "method not allowed")
		return
	}
	var req struct {
		domain.NetworkProfile
		Password *string `json:"password,omitempty"`
	}
	if !decode(w, r, &req) {
		return
	}
	if e := req.Validate(); e != nil {
		problem(w, 400, e.Error())
		return
	}
	secret := ""
	if req.Version > 0 {
		previous, old, e := a.s.NetworkProfile(r.Context(), domain.NetworkVersion{ID: req.ID, Version: req.Version})
		if e != nil {
			failure(w, e)
			return
		}
		if old != "" && req.Password == nil && (previous.HTTPProxy != req.HTTPProxy || previous.HTTPSProxy != req.HTTPSProxy) {
			problem(w, 400, "更换代理地址时请重新提供密码")
			return
		}
		secret = old
	}
	if req.Password != nil {
		if len(*req.Password) > 4096 || strings.ContainsRune(*req.Password, 0) {
			problem(w, 400, "密码无效")
			return
		}
		if *req.Password == "" {
			secret = ""
		} else {
			if a.cfg.Box == nil {
				problem(w, 503, "请配置 SWARMJET_MASTER_KEY")
				return
			}
			var e error
			secret, e = a.cfg.Box.Seal(*req.Password)
			if e != nil {
				failure(w, e)
				return
			}
		}
	}
	req.HasPassword = secret != ""
	p, e := a.s.SaveNetwork(r.Context(), req.NetworkProfile, secret)
	if e != nil {
		failure(w, e)
		return
	}
	respond(w, 200, p)
}
