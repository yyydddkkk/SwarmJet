package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"swarmjet"
	"swarmjet/internal/domain"
	"swarmjet/internal/registrycatalog"
	"swarmjet/internal/registryconfig"
	"swarmjet/internal/store"
)

const platformRepository = "swarmjet/platform"

func (a *server) platformImages(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/registry/platform-images")
	config, access, err := registryconfig.Load(r.Context(), a.s, a.cfg.Box, false)
	if err != nil || config.Mode != "builtin" {
		problem(w, 503, "请先启用内置镜像仓库")
		return
	}
	config.Repository = platformRepository
	if path != "" {
		a.managePlatformImage(w, r, path, config)
		return
	}
	if r.Method != http.MethodGet {
		problem(w, 405, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	images, err := registrycatalog.New(config, access).List(ctx)
	if err != nil {
		problem(w, 502, "程序镜像清单读取失败")
		return
	}
	respond(w, 200, map[string]any{"repository": config.Address + "/" + platformRepository, "images": images})
}

func (a *server) platformImageUsage(ctx context.Context, reference string, image registrycatalog.Image) ([]string, error) {
	usage, err := a.s.PlatformImageUsage(ctx, reference)
	if err != nil {
		return nil, err
	}
	current := a.cfg.PlatformImage
	if current == reference {
		usage = append(usage, "控制面当前程序镜像")
	}
	for _, tag := range image.Tags {
		if current == strings.Split(reference, "@")[0]+":"+tag {
			usage = append(usage, "控制面当前程序镜像")
			break
		}
	}
	return usage, nil
}

func (a *server) managePlatformImage(w http.ResponseWriter, r *http.Request, path string, config registryconfig.Config) {
	options, err := a.loadDeveloperOptions(r)
	if err != nil {
		failure(w, err)
		return
	}
	if !options.PlatformImages {
		problem(w, 403, "请先启用平台镜像管理")
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if !strings.HasPrefix(path, "/") || len(parts) < 1 || len(parts) > 2 || !strings.HasPrefix(parts[0], "sha256:") || len(parts[0]) != 71 {
		problem(w, 404, "平台镜像版本不存在")
		return
	}
	reference := config.Address + "/" + platformRepository + "@" + parts[0]
	if !domain.RepositoryDigest.MatchString(reference) {
		problem(w, 400, "镜像摘要无效")
		return
	}
	_, access, err := registryconfig.Load(r.Context(), a.s, a.cfg.Box, true)
	if err != nil {
		failure(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	images, err := registrycatalog.New(config, access).List(ctx)
	if err != nil {
		problem(w, 502, "平台镜像清单读取失败")
		return
	}
	var image *registrycatalog.Image
	for i := range images {
		if images[i].Digest == parts[0] {
			image = &images[i]
			break
		}
	}
	if image == nil {
		problem(w, 404, "平台镜像版本不存在")
		return
	}
	if len(parts) == 2 && parts[1] == "usage" && r.Method == http.MethodGet {
		usage, err := a.platformImageUsage(ctx, reference, *image)
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, 200, map[string]any{"references": usage})
		return
	}
	if len(parts) != 1 || r.Method != http.MethodDelete {
		problem(w, 405, "method not allowed")
		return
	}
	var req struct {
		ExpectedTags []string `json:"expectedTags"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.ExpectedTags) == 0 {
		problem(w, 400, "请刷新版本清单后再删除")
		return
	}
	sort.Strings(req.ExpectedTags)
	if !reflect.DeepEqual(image.Tags, req.ExpectedTags) {
		problem(w, 409, "镜像标签已变化，请刷新后重试")
		return
	}
	usage, err := a.platformImageUsage(ctx, reference, *image)
	if err != nil {
		failure(w, err)
		return
	}
	if len(usage) > 0 {
		problem(w, 409, "平台镜像仍被使用："+strings.Join(usage, "、"))
		return
	}
	_, err = a.s.WithPlatformImageDeletionGuard(ctx, reference, func() error {
		latest, e := registrycatalog.New(config, access).List(ctx)
		if e != nil {
			return e
		}
		for _, candidate := range latest {
			if candidate.Digest == image.Digest {
				if !reflect.DeepEqual(candidate.Tags, req.ExpectedTags) {
					return store.ErrConflict
				}
				return registrycatalog.New(config, access).Delete(ctx, image.Digest)
			}
		}
		return store.ErrNotFound
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
			failure(w, err)
		} else {
			problem(w, 502, "平台镜像删除失败")
		}
		return
	}
	_ = a.s.Event(r.Context(), "", "platform_image_deleted", "管理员删除平台程序镜像 "+reference)
	respond(w, 204, nil)
}

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

func (v nodeBundleRequest) validate(address string) error {
	if !domain.RepositoryDigest.MatchString(v.Image) || !strings.HasPrefix(v.Image, address+"/"+platformRepository+"@sha256:") {
		return errors.New("请选择内置仓库中已发布的程序镜像")
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
	for _, name := range []string{"install_node.py", "compose.node.yaml", "upgrade_host.py", "Dockerfile.updater", "registry-ca.crt", "node.json", "README.txt"} {
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
	config, access, err := registryconfig.Load(r.Context(), a.s, a.cfg.Box, false)
	if err != nil || config.Mode != "builtin" || access.Username == "" || access.Password == "" {
		problem(w, 503, "内置仓库拉取凭据未配置")
		return
	}
	if err := req.validate(config.Address); err != nil {
		problem(w, 400, err.Error())
		return
	}
	config.Repository = platformRepository
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	images, err := registrycatalog.New(config, access).List(ctx)
	if err != nil {
		problem(w, 502, "程序镜像清单读取失败")
		return
	}
	found := false
	for _, image := range images {
		found = found || image.Reference == req.Image
	}
	if !found {
		problem(w, 400, "程序镜像尚未发布到内置仓库")
		return
	}
	caPath := a.cfg.RegistryCAFile
	if caPath == "" {
		caPath = os.Getenv("REGISTRY_CA_FILE")
	}
	ca, err := os.ReadFile(caPath)
	if err != nil || len(ca) == 0 || len(ca) > 1<<20 || !bytes.Contains(ca, []byte("BEGIN CERTIFICATE")) {
		problem(w, 503, "内置仓库 CA 文件不可用")
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
	upgrader, err := swarmjet.NodeDeploymentAssets.ReadFile("deploy/upgrade_host.py")
	if err != nil {
		failure(w, err)
		return
	}
	updaterDockerfile, err := swarmjet.NodeDeploymentAssets.ReadFile("deploy/Dockerfile.updater")
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
		"registryAddress": config.Address, "registryUser": access.Username, "registryPassword": access.Password,
	}, "", "  ")
	if err != nil {
		failure(w, err)
		return
	}
	readme := fmt.Sprintf("SwarmJet Node %s deployment bundle\n\nKeep this archive private. node.json contains registration and registry pull credentials.\nOn the target Linux server:\n  mkdir -m 700 swarmjet-node-%s\n  tar -xzf swarmjet-node-%s.tar.gz -C swarmjet-node-%s\n  cd swarmjet-node-%s\n  python3 install_node.py --check\n  sudo python3 install_node.py\nThen open Node management, assign an Agent image, configure Runner, and enable the node.\n", id, id, id, id, id)
	bundle, err := makeNodeBundle(map[string][]byte{"install_node.py": installer, "compose.node.yaml": compose, "upgrade_host.py": upgrader, "Dockerfile.updater": updaterDockerfile, "registry-ca.crt": ca, "node.json": settings, "README.txt": []byte(readme)})
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
