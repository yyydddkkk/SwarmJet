package api

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Runtime control stays in the Worker. The console never receives Docker access.
func (a *server) runtime(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/runtime")
	if r.Method != "GET" && !(r.Method == "POST" && path == "/verify") {
		problem(w, 405, "method not allowed")
		return
	}
	if path != "/status" && path != "/models" && path != "/terminal" && path != "/verify" {
		problem(w, 404, "runtime route not found")
		return
	}
	if a.cfg.RuntimeSecret == "" || a.cfg.RuntimeURL == "" {
		problem(w, 503, "执行器环境管理尚未启用，请配置服务端管理连接。")
		return
	}
	target, e := url.Parse(a.cfg.RuntimeURL)
	if e != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		problem(w, 503, "执行器管理连接配置无效。")
		return
	}
	terminal := path == "/terminal"
	if terminal {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			problem(w, 400, "websocket upgrade required")
			return
		}
		originCheck := r.Clone(r.Context())
		originCheck.Header = r.Header.Clone()
		originCheck.Header.Set("X-Requested-With", "swarmjet")
		if !csrfAllowed(originCheck, a.cfg.AllowedOrigin) {
			problem(w, 403, "same-origin terminal required")
			return
		}
	}
	duration := 45 * time.Second
	if terminal {
		duration = time.Hour
	}
	ctx, cancel := context.WithTimeout(r.Context(), duration)
	defer cancel()
	if terminal {
		if a.s != nil {
			_ = a.s.Event(ctx, "", "runtime_terminal_open", "管理员打开执行器配置终端")
		}
		defer func() {
			if a.s != nil {
				c, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = a.s.Event(c, "", "runtime_terminal_close", "执行器配置终端已关闭")
			}
		}()
		go func() {
			tick := time.NewTicker(20 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					if !a.authenticated(r.WithContext(ctx)) {
						cancel()
						return
					}
				}
			}
		}()
	}
	request := r.Clone(ctx)
	request.URL.Path = path
	request.URL.RawPath = ""
	request.URL.RawQuery = ""
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.Host = target.Host
		req.Header.Del("Cookie")
		req.Header.Del("Authorization")
		req.Header.Set("X-SwarmJet-Runtime-Secret", a.cfg.RuntimeSecret)
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		problem(w, 502, "暂时无法连接执行器环境，请检查 Worker 状态后重试。")
	}
	proxy.ServeHTTP(w, request)
}
