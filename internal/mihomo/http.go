package mihomo

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type Handler struct {
	Manager *Manager
	Secret  string
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(v) != nil || d.Decode(&extra) != io.EOF {
		reply(w, 400, map[string]string{"error": "请求格式无效或内容过大"})
		return false
	}
	return true
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		w.WriteHeader(204)
		return
	}
	if len(h.Secret) < 32 || !equal(r.Header.Get("X-SwarmJet-Mihomo-Secret"), h.Secret) {
		reply(w, 401, map[string]string{"error": "代理管理鉴权失败"})
		return
	}
	m := h.Manager
	if m == nil {
		reply(w, 503, map[string]string{"error": "Mihomo 未初始化"})
		return
	}
	fail := func(e error) { reply(w, 400, map[string]string{"error": e.Error()}) }
	switch {
	case r.URL.Path == "/status" && r.Method == http.MethodGet:
		reply(w, 200, m.View())
	case r.URL.Path == "/access" && r.Method == http.MethodGet:
		reply(w, 200, m.access)
	case r.URL.Path == "/import" && r.Method == http.MethodPost:
		var req struct {
			Name    string `json:"name"`
			Format  string `json:"format"`
			Content string `json:"content"`
		}
		if !decode(w, r, &req) {
			return
		}
		rev, e := m.Import(strings.TrimSpace(req.Name), req.Format, req.Content)
		if e != nil {
			fail(e)
			return
		}
		reply(w, 201, rev)
	case strings.HasPrefix(r.URL.Path, "/revisions/") && r.Method == http.MethodGet:
		text, e := m.Configuration(strings.TrimPrefix(r.URL.Path, "/revisions/"))
		if e != nil {
			fail(e)
			return
		}
		reply(w, 200, map[string]string{"yaml": text})
	case r.URL.Path == "/validate" && r.Method == http.MethodPost:
		var req struct {
			ID string `json:"id"`
		}
		if !decode(w, r, &req) {
			return
		}
		if e := m.Validate(r.Context(), req.ID); e != nil {
			fail(e)
			return
		}
		reply(w, 200, map[string]bool{"valid": true})
	case r.URL.Path == "/probe" && r.Method == http.MethodPost:
		var req struct {
			ID    string `json:"id"`
			URL   string `json:"url"`
			Proxy string `json:"proxy"`
		}
		if !decode(w, r, &req) {
			return
		}
		result, e := m.Probe(r.Context(), req.ID, req.URL, req.Proxy)
		if e != nil {
			fail(e)
			return
		}
		reply(w, 200, result)
	case r.URL.Path == "/apply" && r.Method == http.MethodPost:
		var req struct {
			ID string `json:"id"`
		}
		if !decode(w, r, &req) {
			return
		}
		if e := m.Apply(r.Context(), req.ID); e != nil {
			fail(e)
			return
		}
		reply(w, 200, m.View())
	case r.URL.Path == "/rollback" && r.Method == http.MethodPost:
		var req struct {
			URL   string `json:"url"`
			Proxy string `json:"proxy"`
		}
		if !decode(w, r, &req) {
			return
		}
		if e := m.Rollback(r.Context(), req.URL, req.Proxy); e != nil {
			fail(e)
			return
		}
		reply(w, 200, m.View())
	case r.URL.Path == "/groups" && r.Method == http.MethodGet:
		groups, e := m.Groups(r.Context())
		if e != nil {
			fail(e)
			return
		}
		reply(w, 200, map[string]any{"groups": groups})
	case r.URL.Path == "/select" && r.Method == http.MethodPut:
		var req struct {
			Group string `json:"group"`
			Name  string `json:"name"`
		}
		if !decode(w, r, &req) {
			return
		}
		if e := m.Select(r.Context(), req.Group, req.Name); e != nil {
			fail(e)
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	default:
		reply(w, 404, map[string]string{"error": "代理管理接口不存在"})
	}
}
