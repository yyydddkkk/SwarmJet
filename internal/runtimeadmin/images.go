package runtimeadmin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"swarmjet/internal/agentcli"
	"swarmjet/internal/imagebuild"
)

func decodeImageRequest(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeJSON(w, 400, map[string]string{"error": "镜像配置 JSON 无效"})
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "请求只能包含一个 JSON 对象"})
		return false
	}
	return true
}

func (s *server) images(w http.ResponseWriter, r *http.Request) {
	if s.imageManager == nil {
		writeJSON(w, 503, map[string]string{"error": "镜像管理初始化失败，请检查 Worker 数据目录。"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/images")
	fail := func(err error) { writeJSON(w, 400, map[string]string{"error": err.Error()}) }
	switch {
	case path == "/platform" && r.Method == http.MethodGet:
		snapshot := s.imageManager.Snapshot()
		builds := []imagebuild.Build{}
		for _, build := range snapshot.Builds {
			if build.Source == "platform" {
				builds = append(builds, build)
			}
		}
		writeJSON(w, 200, map[string]any{"builds": builds, "available": s.imageManager.ResolvePlatform != nil && s.cfg.PlatformSourceDir != ""})
	case path == "/platform/builds" && r.Method == http.MethodPost:
		var req struct {
			Tag string `json:"tag"`
		}
		if !decodeImageRequest(w, r, &req) {
			return
		}
		build, err := s.imageManager.BuildPlatform(req.Tag, s.cfg.PlatformSourceDir)
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, 202, build)
	case strings.HasPrefix(path, "/platform/builds/"):
		parts := strings.Split(strings.TrimPrefix(path, "/platform/builds/"), "/")
		if len(parts) != 2 {
			writeJSON(w, 404, map[string]string{"error": "构建记录不存在"})
			return
		}
		id, action := parts[0], parts[1]
		found := false
		for _, build := range s.imageManager.Snapshot().Builds {
			if build.ID == id && build.Source == "platform" {
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, 404, map[string]string{"error": "构建记录不存在"})
			return
		}
		switch {
		case action == "log" && r.Method == http.MethodGet:
			log, err := s.imageManager.Logs(id)
			if err != nil {
				fail(err)
				return
			}
			writeJSON(w, 200, map[string]string{"log": log})
		case action == "cancel" && r.Method == http.MethodPost:
			if err := s.imageManager.Cancel(id); err != nil {
				fail(err)
				return
			}
			writeJSON(w, 202, map[string]bool{"ok": true})
		default:
			writeJSON(w, 405, map[string]string{"error": "不支持此请求方法"})
		}
	case path == "" && r.Method == "GET":
		active, err := imagebuild.ReadActive(s.cfg.DataDir)
		if err != nil {
			fail(err)
			return
		}
		scope := "local"
		if s.cfg.ImagesOnly {
			scope = "registry"
		}
		snapshot := s.imageManager.Snapshot()
		writeJSON(w, 200, map[string]any{"config": snapshot.Config, "builds": snapshot.Builds, "active": active, "defaultImage": s.cfg.Image, "agents": agentcli.Catalog(), "parameters": imagebuild.Parameters(), "scope": scope})
	case path == "/config" && r.Method == "PUT":
		var c imagebuild.Config
		if !decodeImageRequest(w, r, &c) {
			return
		}
		if s.cfg.ImagesOnly && c.Repository != "" {
			writeJSON(w, 400, map[string]string{"error": "构建路径由镜像仓库设置统一管理"})
			return
		}
		if err := s.imageManager.SaveConfig(c); err != nil {
			fail(err)
			return
		}
		writeJSON(w, 200, s.imageManager.Snapshot().Config)
	case path == "/builds" && r.Method == "POST":
		var c imagebuild.Config
		if !decodeImageRequest(w, r, &c) {
			return
		}
		b, err := s.imageManager.Build(c)
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, 202, b)
	case path == "/import" && r.Method == "POST":
		if s.cfg.ImagesOnly {
			writeJSON(w, 400, map[string]string{"error": "独立构建服务不访问宿主机镜像；请在节点配置中指定已发布摘要"})
			return
		}
		var req struct {
			Reference string `json:"reference"`
		}
		if !decodeImageRequest(w, r, &req) {
			return
		}
		b, err := s.imageManager.Import(r.Context(), req.Reference)
		if err != nil {
			fail(err)
			return
		}
		writeJSON(w, 201, b)
	default:
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) != 3 || parts[0] != "builds" {
			writeJSON(w, 404, map[string]string{"error": "镜像管理接口不存在"})
			return
		}
		id, action := parts[1], parts[2]
		switch {
		case action == "log" && r.Method == "GET":
			log, err := s.imageManager.Logs(id)
			if err != nil {
				fail(err)
				return
			}
			writeJSON(w, 200, map[string]string{"log": log})
		case action == "cancel" && r.Method == "POST":
			if err := s.imageManager.Cancel(id); err != nil {
				fail(err)
				return
			}
			writeJSON(w, 202, map[string]bool{"ok": true})
		case action == "activate" && r.Method == "POST":
			if s.cfg.ImagesOnly {
				writeJSON(w, 400, map[string]string{"error": "请选择目标节点分发镜像"})
				return
			}
			s.dockerMu.Lock()
			defer s.dockerMu.Unlock()
			if len(s.terminal) > 0 {
				writeJSON(w, 409, map[string]string{"error": "请先关闭配置终端，再切换执行镜像。"})
				return
			}
			active, err := s.imageManager.Activate(r.Context(), id)
			if err != nil {
				fail(err)
				return
			}
			writeJSON(w, 200, active)
		default:
			writeJSON(w, 405, map[string]string{"error": "不支持此请求方法"})
		}
	}
}
