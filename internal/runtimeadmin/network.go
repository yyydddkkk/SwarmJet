package runtimeadmin

import (
	"net/http"
	"strings"
	"swarmjet/internal/networkconfig"
)

func (s *server) checkNetwork(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Target string `json:"target"`
	}
	if !decodeImageRequest(w, r, &req) {
		return
	}
	if e := networkconfig.ValidateTarget(req.Target); e != nil {
		writeJSON(w, 400, map[string]string{"error": e.Error()})
		return
	}
	vars := networkconfig.Direct()
	for _, k := range networkconfig.Keys {
		vars[k] = s.cfg.Env[k]
	}
	type check struct {
		Scope  string `json:"scope"`
		Status int    `json:"status,omitempty"`
		Error  string `json:"error,omitempty"`
	}
	host := check{Scope: "node"}
	code, e := networkconfig.Check(r.Context(), req.Target, vars)
	host.Status = code
	if e != nil {
		host.Error = e.Error()
	}
	container := check{Scope: "agent-container"}
	image, e := s.image(r.Context())
	if e == nil {
		args := []string{"run", "--rm", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=64m", "--pids-limit=32"}
		if s.cfg.DockerNetwork != "" {
			args = append(args, "--network", s.cfg.DockerNetwork)
		}
		for _, k := range networkconfig.Keys {
			args = append(args, "--env", k)
		}
		args = append(args, "--entrypoint", "curl", image, "--silent", "--head", "--max-time", "12", "--output", "/dev/null", "--write-out", "%{http_code}", "--", req.Target)
		raw, err := s.docker(r.Context(), vars, args...)
		if err != nil {
			container.Error = "容器连接失败，请检查镜像是否包含 curl、代理和证书"
		} else {
			for _, c := range strings.TrimSpace(string(raw)) {
				if c >= '0' && c <= '9' {
					container.Status = container.Status*10 + int(c-'0')
				}
			}
		}
	} else {
		container.Error = "节点镜像不可用"
	}
	writeJSON(w, 200, map[string]any{"checks": []check{host, container}})
}
