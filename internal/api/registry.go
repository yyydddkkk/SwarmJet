package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"swarmjet/internal/registryconfig"
	"swarmjet/internal/store"
)

func (a *server) registry(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/registry/reveal" {
		if r.Method != http.MethodPost {
			problem(w, 405, "method not allowed")
			return
		}
		_, push, err := registryconfig.Load(r.Context(), a.s, a.cfg.Box, true)
		if err != nil {
			failure(w, err)
			return
		}
		_, pull, err := registryconfig.Load(r.Context(), a.s, a.cfg.Box, false)
		if err != nil {
			failure(w, err)
			return
		}
		_ = a.s.Event(r.Context(), "", "registry_credentials_revealed", "管理员查看镜像仓库凭据")
		respond(w, 200, map[string]string{"pushPassword": push.Password, "pullPassword": pull.Password})
		return
	}
	var saved registryconfig.Saved
	e := a.s.Setting(r.Context(), "registry", &saved)
	if e != nil && !errors.Is(e, store.ErrNotFound) {
		failure(w, e)
		return
	}
	if r.Method == "GET" {
		config, _, err := registryconfig.Load(r.Context(), a.s, a.cfg.Box, false)
		if errors.Is(err, store.ErrNotFound) {
			config = registryconfig.Config{Mode: "custom", Repository: "swarmjet/agent"}
		} else if err != nil {
			failure(w, err)
			return
		}
		respond(w, 200, config)
		return
	}
	if r.Method != "PUT" {
		problem(w, 405, "method not allowed")
		return
	}
	var req struct {
		registryconfig.Config
		PushPassword *string `json:"pushPassword"`
		PullPassword *string `json:"pullPassword"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Mode == "builtin" {
		builtin, ok := registryconfig.Builtin()
		if !ok {
			problem(w, 400, "内置镜像仓库未启用")
			return
		}
		if e = a.s.SetSetting(r.Context(), "registry", registryconfig.Saved{Config: builtin}); e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, builtin)
		return
	}
	req.Mode = "custom"
	if builtin, ok := registryconfig.Builtin(); ok {
		req.BuiltinAvailable = true
		req.BuiltinAddress = builtin.Address
	}
	if e = req.Validate(); e != nil {
		problem(w, 400, e.Error())
		return
	}
	if a.cfg.Box == nil {
		problem(w, 503, "请配置 SWARMJET_MASTER_KEY")
		return
	}
	var sec registryconfig.Secrets
	if saved.Secret != "" {
		raw, err := a.cfg.Box.Open(saved.Secret)
		if err != nil {
			failure(w, err)
			return
		}
		if e = json.Unmarshal([]byte(raw), &sec); e != nil {
			failure(w, e)
			return
		}
	}
	if saved.Config.Address != "" && saved.Config.Address != req.Address && (req.PushPassword == nil || req.PullPassword == nil) {
		problem(w, 400, "更换仓库地址时请重新提供凭据")
		return
	}
	if req.PushPassword != nil {
		sec.Push = *req.PushPassword
	}
	if req.PullPassword != nil {
		sec.Pull = *req.PullPassword
	}
	raw, e := json.Marshal(sec)
	if e != nil {
		failure(w, e)
		return
	}
	sealed, e := a.cfg.Box.Seal(string(raw))
	if e != nil {
		failure(w, e)
		return
	}
	req.HasPushPassword = sec.Push != ""
	req.HasPullPassword = sec.Pull != ""
	if e = a.s.SetSetting(r.Context(), "registry", registryconfig.Saved{Config: req.Config, Secret: sealed}); e != nil {
		failure(w, e)
		return
	}
	respond(w, 200, req.Config)
}
