package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type nodeBundleRequest struct {
	Image           string `json:"image"`
	DataDir         string `json:"dataDir"`
	AdvertiseURL    string `json:"advertiseUrl"`
	ControlPlaneURL string `json:"controlPlaneUrl"`
	Capacity        int    `json:"capacity"`
	Port            int    `json:"port"`
	ListenHost      string `json:"listenHost"`
}

func validNodeURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/") && !strings.ContainsAny(raw, " \t\r\n$#'\"")
}

func (v nodeBundleRequest) validate() error {
	if !domain.ImageReference.MatchString(v.Image) {
		return errors.New("请填写有效的程序镜像地址")
	}
	if !filepath.IsAbs(v.DataDir) || strings.ContainsAny(v.DataDir, " \t\r\n$#'\"\\") || strings.Contains(v.DataDir, "..") {
		return errors.New("节点数据目录必须是安全的绝对路径")
	}
	if !validNodeURL(v.AdvertiseURL) || !validNodeURL(v.ControlPlaneURL) {
		return errors.New("请填写两端互相可访问的 HTTP(S) 地址")
	}
	if v.Capacity < 1 || v.Capacity > 128 || v.Port < 1 || v.Port > 65535 {
		return errors.New("容量或端口无效")
	}
	if v.ListenHost != "0.0.0.0" && v.ListenHost != "127.0.0.1" {
		return errors.New("监听地址无效")
	}
	return nil
}

func makeNodeBundle(files map[string][]byte) ([]byte, error) {
	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	tw := tar.NewWriter(zw)
	createdAt := time.Now().UTC().Truncate(time.Second)
	for _, name := range []string{"install_node.py", "compose.node.yaml", "node.json", "README.txt"} {
		content := files[name]
		mode := int64(0644)
		if name == "node.json" {
			mode = 0600
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(content)), ModTime: createdAt}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

func (a *server) nodeDeployment(w http.ResponseWriter, r *http.Request, id string) {
	if a.s == nil || a.cfg.Box == nil {
		problem(w, 503, "节点注册凭据未配置")
		return
	}
	var req nodeBundleRequest
	if !decode(w, r, &req) {
		return
	}
	statuses, err := a.s.NodeStatuses(r.Context())
	if err != nil {
		failure(w, err)
		return
	}
	for _, status := range statuses {
		if status.Node.ID == id {
			problem(w, 409, "该节点已注册；请使用新的节点 ID")
			return
		}
	}
	if err := req.validate(); err != nil {
		problem(w, 400, err.Error())
		return
	}
	installer, err := swarmjet.NodeDeploymentAssets.ReadFile("deploy/install_node.py")
	if err != nil {
		failure(w, err)
		return
	}
	compose, err := swarmjet.NodeDeploymentAssets.ReadFile("deploy/compose.node.yaml")
	if err != nil {
		failure(w, err)
		return
	}
	secret, err := opaqueToken()
	if err != nil {
		failure(w, err)
		return
	}
	settings, err := json.MarshalIndent(map[string]any{
		"nodeId": id, "nodeSecret": secret, "image": req.Image,
		"dataDir": req.DataDir, "advertiseUrl": req.AdvertiseURL, "controlPlaneUrl": req.ControlPlaneURL,
		"capacity": req.Capacity, "port": req.Port, "listenHost": req.ListenHost,
	}, "", "  ")
	if err != nil {
		failure(w, err)
		return
	}
	readme := fmt.Sprintf("SwarmJet Node %s deployment bundle\n\nKeep this archive private. node.json contains the node registration secret.\nOn the target Linux server:\n  mkdir -m 700 swarmjet-node-%s\n  tar -xzf swarmjet-node-%s.tar.gz -C swarmjet-node-%s\n  cd swarmjet-node-%s\n  python3 install_node.py --check\n  sudo python3 install_node.py\nThen open Node management, assign an Agent image, configure Runner, and enable the node.\n", id, id, id, id, id)
	bundle, err := makeNodeBundle(map[string][]byte{"install_node.py": installer, "compose.node.yaml": compose, "node.json": settings, "README.txt": []byte(readme)})
	if err != nil {
		failure(w, err)
		return
	}
	sealed, err := a.cfg.Box.Seal(secret)
	if err != nil {
		failure(w, err)
		return
	}
	if err := a.s.SaveNodeCredential(r.Context(), id, sealed, false); err != nil {
		failure(w, err)
		return
	}
	_ = a.s.Event(r.Context(), "", "node_deployment_bundle_created", "管理员生成节点部署包："+id)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "swarmjet-node-"+id+".tar.gz"))
	w.WriteHeader(200)
	_, _ = w.Write(bundle)
}
