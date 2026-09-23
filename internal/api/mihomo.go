package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"swarmjet/internal/domain"
	"swarmjet/internal/mihomo"
	"time"
)

// Only a small authenticated management surface is exposed, never the raw core API.
func (a *server) mihomo(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/mihomo")
	if a.cfg.MihomoURL == "" || a.cfg.MihomoSecret == "" {
		if path == "/status" && r.Method == "GET" {
			respond(w, 200, map[string]any{"enabled": false, "revisions": []any{}, "message": "Mihomo 为可选服务，请按部署说明启用 compose.mihomo.yaml"})
			return
		}
		problem(w, 503, "尚未启用受管 Mihomo 服务")
		return
	}
	if path == "/network" && r.Method == "POST" {
		a.mihomoNetwork(w, r)
		return
	}
	allowed := map[string]string{"/status": "GET", "/import": "POST", "/validate": "POST", "/probe": "POST", "/apply": "POST", "/rollback": "POST", "/groups": "GET", "/select": "PUT"}
	method, ok := allowed[path]
	if strings.HasPrefix(path, "/revisions/") && len(strings.TrimPrefix(path, "/revisions/")) == 32 {
		method = "GET"
		ok = true
	}
	if !ok {
		problem(w, 404, "代理管理接口不存在")
		return
	}
	if r.Method != method {
		problem(w, 405, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	ctx, cancel := context.WithTimeout(r.Context(), 115*time.Second)
	defer cancel()
	resp, e := a.callMihomo(ctx, path, r.Method, r.Body)
	if e != nil {
		problem(w, 502, "无法连接 Mihomo 管理服务，请检查部署和管理密钥")
		return
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if e != nil || len(raw) > 2<<20 {
		problem(w, 502, "代理管理响应超出限制")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}
func (a *server) callMihomo(ctx context.Context, path, method string, body io.Reader) (*http.Response, error) {
	base, e := url.Parse(a.cfg.MihomoURL)
	if e != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, fmt.Errorf("invalid proxy admin URL")
	}
	base.Path = strings.TrimRight(base.Path, "/") + path
	req, e := http.NewRequestWithContext(ctx, method, base.String(), body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("X-SwarmJet-Mihomo-Secret", a.cfg.MihomoSecret)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Transport: mihomoControlTransport, Timeout: 115 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client.Do(req)
}
func (a *server) mihomoNetwork(w http.ResponseWriter, r *http.Request) {
	if a.cfg.Box == nil || a.s == nil {
		problem(w, 503, "请配置持久主密钥和数据库")
		return
	}
	var req struct {
		Name     string `json:"name"`
		Endpoint string `json:"endpoint"`
		NoProxy  string `json:"noProxy"`
	}
	if !decode(w, r, &req) {
		return
	}
	id, e := newID()
	if e != nil {
		failure(w, e)
		return
	}
	p := domain.NetworkProfile{ID: "mihomo-" + id, Name: req.Name, HTTPProxy: req.Endpoint, HTTPSProxy: req.Endpoint, NoProxy: req.NoProxy, HasPassword: true}
	if e = p.Validate(); e != nil {
		problem(w, 400, e.Error())
		return
	}
	if req.Endpoint == "" {
		problem(w, 400, "请填写从构建服务和节点可达的代理入口地址")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	resp, e := a.callMihomo(ctx, "/access", http.MethodGet, nil)
	if e != nil {
		problem(w, 502, "无法读取代理入口认证")
		return
	}
	defer resp.Body.Close()
	var access mihomo.Access
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&access) != nil || access.Username == "" || access.Password == "" {
		problem(w, 502, "代理入口认证不可用")
		return
	}
	p.Username = access.Username
	sealed, e := a.cfg.Box.Seal(access.Password)
	if e != nil {
		failure(w, e)
		return
	}
	p, e = a.s.SaveNetwork(ctx, p, sealed)
	if e != nil {
		failure(w, e)
		return
	}
	respond(w, 201, p)
}

// Control traffic must not inherit an outbound proxy and expose the admin token.
var mihomoControlTransport = &http.Transport{Proxy: nil}
