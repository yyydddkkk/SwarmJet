package runtimeadmin

import (
	"net/http"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/agentcli"
)

// testRunner starts the selected image as a short-lived, restricted container.
// It checks that the image can start and exposes supported CLI executables;
// model credentials and a full task workspace are deliberately not exercised.
func (s *server) testRunner(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "不支持此请求方法"})
		return
	}
	image, err := s.currentImage()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "当前 Runner 镜像配置不可读"})
		return
	}
	if _, err = s.image(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "当前 Runner 镜像不可用"})
		return
	}
	script := ""
	for _, spec := range agentcli.Catalog() {
		script += "if command -v " + spec.Executable + " >/dev/null 2>&1; then printf '%s\\n' '" + spec.ID + "'; fi;\n"
	}
	args := []string{"run", "--rm", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=64m", "--pids-limit=32", "--entrypoint", "/bin/sh", image, "-c", script}
	out, err := s.docker(r.Context(), nil, args...)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Runner 启动失败，请检查节点镜像和 Docker 状态"})
		return
	}
	found := map[string]bool{}
	for _, id := range strings.Fields(string(out)) {
		found[id] = true
	}
	agents := []string{}
	for _, spec := range agentcli.Catalog() {
		if found[spec.ID] {
			agents = append(agents, spec.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": len(agents) > 0, "image": image, "agents": agents})
}
