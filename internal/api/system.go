package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

)

type systemCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

func (a *server) system(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/system" {
		if r.Method != http.MethodGet {
			problem(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		listen := os.Getenv("SWARMJET_HOST_LISTEN_ADDRESS")
		if listen == "" {
			listen = os.Getenv("LISTEN_ADDRESS")
		}
		mode := "local"
		if a.cfg.RequireNode {
			mode = "distributed"
		}
		respond(w, http.StatusOK, map[string]any{
			"executionMode":  mode,
			"publicOrigin":   a.cfg.AllowedOrigin,
			"hostListen":     listen,
			"secureCookies":  a.cfg.SecureCookies,
			"mihomoEnabled":  a.cfg.MihomoURL != "" && a.cfg.MihomoSecret != "",
			// Defaults offered when configuring nodes; administrators may enter any image reference.
			"agentImage":    a.cfg.DefaultAgentImage,
			"platformImage": a.cfg.PlatformImage,
		})
		return
	}
	if r.Method != http.MethodPost {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	mihomoCtx, stopMihomo := context.WithTimeout(r.Context(), 5*time.Second)
	mihomo := a.mihomoCheck(mihomoCtx)
	stopMihomo()
	checks := []systemCheck{mihomo}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if a.s == nil {
		checks = append(checks, systemCheck{ID: "nodes", Status: "unavailable", Message: "节点数据库不可用"})
	} else if nodes, err := a.s.NodeStatuses(ctx); err != nil {
		checks = append(checks, systemCheck{ID: "nodes", Status: "unavailable", Message: "无法读取节点心跳"})
	} else {
		online := 0
		for _, node := range nodes {
			if node.Online {
				online++
			}
		}
		status := "ok"
		if len(nodes) == 0 {
			status = "unavailable"
		} else if online < len(nodes) {
			status = "warning"
		}
		checks = append(checks, systemCheck{ID: "nodes", Status: status, Message: fmt.Sprintf("最近 30 秒有 %d/%d 个节点上报心跳", online, len(nodes))})
	}
	respond(w, http.StatusOK, map[string]any{"checks": checks, "checkedAt": time.Now().UTC()})
}

func (a *server) mihomoCheck(ctx context.Context) systemCheck {
	result := systemCheck{ID: "mihomo", Status: "unavailable", Message: "未启用受管代理服务"}
	if a.cfg.MihomoURL == "" || a.cfg.MihomoSecret == "" {
		return result
	}
	resp, err := a.callMihomo(ctx, "/status", http.MethodGet, nil)
	if err != nil {
		result.Status = "error"
		result.Message = "控制面无法连接 Mihomo 管理服务"
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		result.Status = "error"
		result.Message = fmt.Sprintf("Mihomo 管理服务返回 HTTP %d", resp.StatusCode)
		return result
	}
	result.Status = "ok"
	result.Message = "控制面已连接 Mihomo 管理服务；代理出口仍需单独测试"
	return result
}
