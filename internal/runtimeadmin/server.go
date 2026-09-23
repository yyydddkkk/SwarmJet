// Package runtimeadmin exposes a private, authenticated setup terminal for one
// persistent runtime profile. Browser authentication/origin policy belongs to the API proxy.
package runtimeadmin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"swarmjet/internal/agentcli"
	"swarmjet/internal/imagebuild"
	"sync"
	"time"
)

type DockerRunner func(context.Context, []string, []string) ([]byte, error)
type Config struct {
	ConfigureImages        func(*imagebuild.Manager) error
	ImagesOnly             bool
	DisableImages          bool
	DockerNetwork          string
	Revision               string
	ImageRunner            imagebuild.Runner
	DataDir, Image, Secret string
	Env                    map[string]string
	DockerCommand          DockerRunner
	TerminalCommand        func(context.Context, []string, []string) *exec.Cmd
}
type server struct {
	imageManager   *imagebuild.Manager
	cfg            Config
	dirs           ProfileDirs
	name, identity string
	secret         [32]byte
	dockerMu       sync.Mutex
	terminal       chan struct{}
	initErr        error
}

func New(cfg Config) http.Handler {
	missingDir := cfg.DataDir == ""
	abs, e := filepath.Abs(cfg.DataDir)
	if e == nil {
		cfg.DataDir = abs
	}
	sum := sha256.Sum256([]byte(abs))
	identity := hex.EncodeToString(sum[:12])
	s := &server{cfg: cfg, dirs: GetProfileDirs(abs), name: "sj-config-" + identity, identity: identity, secret: sha256.Sum256([]byte(cfg.Secret)), terminal: make(chan struct{}, 1), initErr: e}
	if missingDir || (cfg.Image == "" && !cfg.ImagesOnly) || strings.HasPrefix(cfg.Image, "-") || strings.ContainsAny(abs, ",\n\r") {
		s.initErr = errors.New("runtime profile configuration is incomplete")
	}
	safe := map[string]string{}
	for k, v := range cfg.Env {
		isProxy := false
		for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy"} {
			if k == name {
				isProxy = true
			}
		}
		if _, ok := providerEnv[k]; isProxy || (ok || k == "OPENCODE_CONFIG_CONTENT") && v != "" {
			if k == "OPENCODE_CONFIG_CONTENT" && len(v) > maxInlineConfig {
				s.initErr = errors.New("inline provider configuration exceeds size limit")
				continue
			}
			safe[k] = v
		}
	}
	s.cfg.Env = safe
	if s.initErr == nil && !cfg.DisableImages {
		s.imageManager, _ = imagebuild.New(cfg.DataDir, cfg.ImageRunner)
		if s.imageManager != nil && cfg.ConfigureImages != nil {
			if err := cfg.ConfigureImages(s.imageManager); err != nil {
				s.initErr = err
				s.imageManager = nil
			}
		}
	}
	return s
}
func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ImagesOnly && !strings.HasPrefix(r.URL.Path, "/images") {
		writeJSON(w, 404, map[string]string{"error": "构建服务仅提供镜像接口"})
		return
	}
	provided := sha256.Sum256([]byte(r.Header.Get("X-SwarmJet-Runtime-Secret")))
	if s.cfg.Secret == "" || subtle.ConstantTimeCompare(provided[:], s.secret[:]) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "运行环境鉴权失败"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/images" || strings.HasPrefix(r.URL.Path, "/images/") {
		s.images(w, r)
		return
	}
	if r.Method != http.MethodGet && !(r.Method == http.MethodPost && (r.URL.Path == "/verify" || r.URL.Path == "/network-test" || r.URL.Path == "/runner-test")) {
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "不支持此请求方法"})
		return
	}
	if s.initErr != nil && r.URL.Path != "/network-test" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "运行环境尚未正确配置"})
		return
	}
	switch r.URL.Path {
	case "/network-test":
		s.checkNetwork(w, r)
	case "/status":
		s.status(w, r)
	case "/models":
		s.models(w, r)
	case "/terminal":
		s.serveTerminal(w, r)
	case "/verify":
		s.verify(w, r)
	case "/runner-test":
		s.testRunner(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "接口不存在"})
	}
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func processEnv(extra map[string]string) []string {
	env := []string{}
	for _, k := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	keys := []string{}
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+extra[k])
	}
	return env
}

type limitedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxOutput - b.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func defaultDocker(ctx context.Context, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = env
	var out limitedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	e := cmd.Run()
	if out.overflow {
		return nil, errors.New("Docker output exceeded limit")
	}
	return out.Bytes(), e
}
func (s *server) docker(ctx context.Context, extra map[string]string, args ...string) ([]byte, error) {
	return s.dockerTimeout(ctx, 30*time.Second, extra, args...)
}

// dockerTimeout bounds a single Docker invocation. Model verification needs a
// longer budget than container inspection.
func (s *server) dockerTimeout(ctx context.Context, timeout time.Duration, extra map[string]string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	run := s.cfg.DockerCommand
	if run == nil {
		run = defaultDocker
	}
	b, e := run(ctx, args, processEnv(extra))
	if len(b) > maxOutput {
		return nil, errors.New("Docker output exceeded limit")
	}
	return b, e
}

type profileContainer struct {
	Name, Image string
	Config      struct {
		Image  string
		User   string
		Labels map[string]string
	}
	State struct {
		Running bool
		Status  string
	}
}

func (s *server) inspect(ctx context.Context) (profileContainer, bool, error) {
	var c profileContainer
	b, e := s.docker(ctx, nil, "container", "ls", "-a", "--filter", "name=^/"+s.name+"$", "--format", "{{.Names}}")
	if e != nil {
		return c, false, errors.New("无法连接 Docker")
	}
	if strings.TrimSpace(string(b)) == "" {
		return c, false, nil
	}
	b, e = s.docker(ctx, nil, "container", "inspect", s.name)
	if e != nil {
		return c, false, errors.New("无法读取运行环境容器状态")
	}
	var list []profileContainer
	if json.Unmarshal(b, &list) != nil || len(list) != 1 {
		return c, false, errors.New("运行环境容器状态无效")
	}
	c = list[0]
	if strings.TrimPrefix(c.Name, "/") != s.name || c.Config.Labels["swarmjet.runtime-profile"] != "default" || c.Config.Labels["swarmjet.runtime-root"] != s.identity {
		return c, false, errors.New("同名容器不属于此运行环境，已拒绝操作")
	}
	return c, true, nil
}
func (s *server) currentImage() (string, error) {
	if s.cfg.DockerNetwork != "" {
		return s.cfg.Image, nil
	}
	return imagebuild.CurrentImage(s.cfg.DataDir, s.cfg.Image)
}
func (s *server) image(ctx context.Context) (string, error) {
	configured, e := s.currentImage()
	if e != nil {
		return "", e
	}
	b, e := s.docker(ctx, nil, "image", "inspect", "--format", "{{.Id}}", configured)
	if e != nil || strings.TrimSpace(string(b)) == "" {
		return "", errors.New("运行环境镜像不可用")
	}
	return strings.TrimSpace(string(b)), nil
}
func (s *server) ensure(ctx context.Context) error {
	s.dockerMu.Lock()
	defer s.dockerMu.Unlock()
	if e := ensureProfileHome(s.dirs.Home); e != nil {
		return errors.New("运行环境配置目录不可用。")
	}
	configured, e := s.currentImage()
	if e != nil {
		return e
	}
	image, e := s.image(ctx)
	if e != nil {
		return e
	}
	c, exists, e := s.inspect(ctx)
	if e != nil {
		return e
	}
	if exists && ((s.cfg.Revision != "" && c.Config.Labels["swarmjet.runtime-revision"] != s.cfg.Revision) || c.Config.Image != configured || c.Config.User != runtimeUser() || (c.Image != "" && c.Image != image)) {
		if _, e = s.docker(ctx, nil, "container", "stop", "--time", "5", s.name); e != nil {
			return errors.New("无法停止旧版本运行环境容器")
		}
		if _, e = s.docker(ctx, nil, "container", "rm", s.name); e != nil {
			return errors.New("无法替换旧版本运行环境容器")
		}
		exists = false
	}
	if !exists {
		args := []string{"container", "create", "--name", s.name, "--user", runtimeUser(), "--label", "swarmjet.runtime-profile=default", "--label", "swarmjet.runtime-root=" + s.identity, "--label", "swarmjet.runtime-revision=" + s.cfg.Revision, "--mount", "type=bind,src=" + s.dirs.Home + ",dst=/runtime-home", "--workdir", "/runtime-home", "--env", "HOME=/runtime-home", "--env", "XDG_DATA_HOME=/runtime-home/.local/share", "--env", "XDG_CONFIG_HOME=/runtime-home/.config", "--env", "CODEX_HOME=/runtime-home/.codex", "--env", "HISTFILE=/dev/null", "--env", "HISTSIZE=0", "--env", "HISTFILESIZE=0"}
		if s.cfg.DockerNetwork != "" {
			args = append(args, "--network", s.cfg.DockerNetwork)
		}
		keys := []string{}
		for k := range s.cfg.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "--env", k)
		}
		args = append(args, "--entrypoint", "/bin/sh", configured, "-c", "exec sleep infinity")
		if _, e = s.docker(ctx, s.cfg.Env, args...); e != nil {
			return errors.New("无法创建运行环境容器")
		}
		c.State.Running = false
	}
	if !c.State.Running {
		if _, e = s.docker(ctx, nil, "container", "start", s.name); e != nil {
			return errors.New("无法启动运行环境容器")
		}
	}
	return nil
}
func (s *server) stop(ctx context.Context) error {
	s.dockerMu.Lock()
	defer s.dockerMu.Unlock()
	c, exists, e := s.inspect(ctx)
	if e != nil {
		return e
	}
	if !exists || !c.State.Running {
		return nil
	}
	if _, e = s.docker(ctx, nil, "container", "stop", "--time", "5", s.name); e != nil {
		if _, e = s.docker(ctx, nil, "container", "kill", s.name); e != nil {
			return errors.New("无法停止配置终端容器")
		}
	}
	return nil
}
func (s *server) installed(ctx context.Context) (map[string]bool, bool) {
	script := ""
	for _, spec := range agentcli.Catalog() {
		script += "command -v " + spec.Executable + " >/dev/null 2>&1 && printf '" + spec.ID + "\\n'; "
	}
	script += "true"
	b, e := s.docker(ctx, nil, "exec", s.name, "/bin/sh", "-c", script)
	found := map[string]bool{}
	if e != nil {
		return found, false
	}
	for _, name := range strings.Fields(string(b)) {
		found[name] = true
	}
	return found, true
}

type agentStatus struct {
	ID                 string `json:"id"`
	Installed          bool   `json:"installed"`
	Configured         bool   `json:"configured"`
	ExecutionSupported bool   `json:"executionSupported"`
}

type openCodeStatus struct {
	Installed    bool     `json:"installed"`
	Configured   bool     `json:"configured"`
	Providers    []string `json:"providers"`
	ConfigExists bool     `json:"configExists"`
}
type codexStatus struct {
	Installed          bool `json:"installed"`
	Configured         bool `json:"configured"`
	ExecutionSupported bool `json:"executionSupported"`
}
type statusResponse struct {
	Agents       []agentStatus  `json:"agents"`
	Profile      string         `json:"profile"`
	Image        string         `json:"image"`
	Available    bool           `json:"available"`
	ToolsChecked bool           `json:"toolsChecked"`
	OpenCode     openCodeStatus `json:"opencode"`
	Codex        codexStatus    `json:"codex"`
	Message      string         `json:"message,omitempty"`
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	configured, err := s.currentImage()
	if err != nil {
		writeJSON(w, 503, map[string]string{"error": "当前镜像配置不可读"})
		return
	}
	metadata := profileInfo(s.dirs, s.cfg.Env)
	out := statusResponse{Profile: "default", Image: configured, OpenCode: openCodeStatus{Configured: len(metadata.Providers) > 0, Providers: metadata.Providers, ConfigExists: metadata.ConfigExists}, Codex: codexStatus{Configured: metadata.CodexConfigured, ExecutionSupported: true}}
	for _, spec := range agentcli.Catalog() {
		configured := false
		for _, name := range spec.Files {
			if b, e := readProfileFile(s.dirs.Home, name); e == nil && len(b) > 0 {
				configured = true
			}
		}
		out.Agents = append(out.Agents, agentStatus{ID: spec.ID, Configured: configured, ExecutionSupported: true})
	}
	if _, e := s.image(r.Context()); e != nil {
		out.Message = "配置的运行环境镜像不可用。"
		writeJSON(w, 200, out)
		return
	}
	out.Available = true
	c, exists, e := s.inspect(r.Context())
	if e != nil {
		out.Available = false
		out.Message = e.Error()
	} else if exists && c.State.Running {
		if c.Config.Image != configured {
			out.Message = "配置终端将使用当前指定镜像重新创建，已保存的配置会保留。"
		} else {
			installed, checked := s.installed(r.Context())
			out.ToolsChecked = checked
			out.OpenCode.Installed, out.Codex.Installed = installed["opencode"], installed["codex"]
			for i := range out.Agents {
				out.Agents[i].Installed = installed[out.Agents[i].ID]
			}
		}
	} else {
		out.Message = "打开终端或刷新模型后，将启动配置环境并检测已安装的工具。"
	}
	if len(metadata.Warnings) > 0 {
		out.Message = strings.Join(metadata.Warnings, " ")
	}
	writeJSON(w, 200, out)
}

var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.:/+@-]*$`)
var ansi = regexp.MustCompile("\\x1b\\[[0-?]*[ -/]*[@-~]")

func (s *server) models(w http.ResponseWriter, r *http.Request) {
	if e := s.ensure(r.Context()); e != nil {
		writeJSON(w, 503, map[string]string{"error": e.Error()})
		return
	}
	metadata := profileInfo(s.dirs, s.cfg.Env)
	response := struct {
		Models    []string `json:"models"`
		Providers []string `json:"providers"`
		Warnings  []string `json:"warnings"`
		Error     string   `json:"error,omitempty"`
	}{Models: []string{}, Providers: metadata.Providers, Warnings: metadata.Warnings}
	response.Warnings = append(response.Warnings, "模型目录仅供选择，不代表凭证、余额、配额或模型访问权限已验证。")
	if len(metadata.Providers) == 0 {
		response.Warnings = append(response.Warnings, "尚未发现已配置的 OpenCode 提供商。")
		writeJSON(w, 200, response)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	b, e := s.docker(ctx, nil, "exec", "--workdir", "/runtime-home", s.name, "opencode", "models")
	if e != nil {
		response.Error = "无法读取 OpenCode 模型目录，请检查工具安装和提供商配置。"
		response.Warnings = append(response.Warnings, response.Error)
		writeJSON(w, 502, response)
		return
	}
	allowed := map[string]bool{}
	for _, p := range metadata.Providers {
		allowed[p] = true
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(ansi.ReplaceAllString(string(b), ""), "\n") {
		line = strings.TrimSpace(line)
		if len(line) > 512 || !modelID.MatchString(line) {
			continue
		}
		provider, _, _ := strings.Cut(line, "/")
		if allowed[provider] && !seen[line] {
			seen[line] = true
			response.Models = append(response.Models, line)
		}
	}
	sort.Strings(response.Models)
	writeJSON(w, 200, response)
}

func runtimeUser() string { return strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid()) }

type verifyRequest struct {
	Model string `json:"model"`
}

// verify performs one minimal real model call. A detected model catalog is not
// proof that a credential or quota is usable, so the console must exercise the
// exact model before a project is enabled.
func (s *server) verify(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式无效。"})
		return
	}
	model := strings.TrimSpace(req.Model)
	if !modelID.MatchString(model) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "模型标识无效，请使用 provider/model 格式。"})
		return
	}
	if err := s.ensure(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	started := time.Now()
	_, err := s.dockerTimeout(ctx, 95*time.Second, nil, "exec", "--workdir", "/runtime-home", s.name, "opencode", "run", "-m", model, "Reply with the single word OK.")
	if ctx.Err() == context.DeadlineExceeded {
		writeJSON(w, http.StatusGatewayTimeout, map[string]string{"error": "模型验证超时（90 秒），请检查网络、代理或服务商状态。"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "模型调用失败，请确认登录有效且该模型可用。"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": model, "message": "最小实际调用成功。", "durationSeconds": int(time.Since(started).Seconds())})
}

// TerminalActive allows the node's versioned handler to preserve open terminals.
func (s *server) TerminalActive() bool { return len(s.terminal) > 0 }
