package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"swarmjet/internal/registrycatalog"
	"swarmjet/internal/registryconfig"
	"swarmjet/internal/store"
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
			"builderEnabled": a.cfg.BuilderURL != "",
			"mihomoEnabled":  a.cfg.MihomoURL != "" && a.cfg.MihomoSecret != "",
		})
		return
	}
	if r.Method != http.MethodPost {
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	registryCtx, stopRegistry := context.WithTimeout(r.Context(), 5*time.Second)
	registry := a.registryCheck(registryCtx)
	stopRegistry()
	mihomoCtx, stopMihomo := context.WithTimeout(r.Context(), 5*time.Second)
	mihomo := a.mihomoCheck(mihomoCtx)
	stopMihomo()
	checks := []systemCheck{registry, mihomo}
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

func (a *server) registryCheck(ctx context.Context) systemCheck {
	result := systemCheck{ID: "registry", Status: "unavailable", Message: "尚未配置镜像仓库"}
	if a.s == nil {
		return result
	}
	config, access, err := registryconfig.Load(ctx, a.s, a.cfg.Box, false)
	if errors.Is(err, store.ErrNotFound) {
		return result
	}
	if err != nil {
		result.Message = "读取仓库配置失败"
		return result
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+config.Address+"/v2/", nil)
	if err != nil {
		result.Message = "仓库地址无效"
		return result
	}
	req.SetBasicAuth(access.Username, access.Password)
	client := registrycatalog.New(config, access).HTTP
	resp, err := client.Do(req)
	if err != nil {
		result.Status = "error"
		result.Message = registryConnectionError(err)
		return result
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		result.Status = "error"
		result.Message = fmt.Sprintf("仓库返回 HTTP %d，请检查地址、TLS 与拉取凭据", resp.StatusCode)
		return result
	}
	result.Status = "ok"
	result.Message = "控制面已通过 TLS 与拉取认证连接仓库"
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		expires := resp.TLS.PeerCertificates[0].NotAfter
		if time.Until(expires) < 30*24*time.Hour {
			result.Status = "warning"
			result.Message = "仓库连接正常，但 TLS 证书将于 " + expires.Format("2006-01-02") + " 到期"
		}
	}
	return result
}

func registryConnectionError(err error) string {
	var dns *net.DNSError
	var cert *tls.CertificateVerificationError
	var hostname x509.HostnameError
	var unknownCA x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var network *net.OpError
	switch {
	case errors.As(err, &dns):
		return "仓库域名解析失败，请检查控制面的 DNS"
	case errors.As(err, &cert), errors.As(err, &hostname), errors.As(err, &unknownCA), errors.As(err, &invalid):
		return "仓库 TLS 验证失败，请检查证书名称、有效期和 CA 信任"
	case errors.Is(err, context.DeadlineExceeded):
		return "连接仓库超时，请检查地址、端口和网络路由"
	case errors.As(err, &network):
		return "无法连接仓库端口，请检查监听地址和网络路由"
	default:
		return "控制面连接仓库失败，请检查 DNS、端口、TLS 与代理配置"
	}
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
