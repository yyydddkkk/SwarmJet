package imagebuild

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	assets "swarmjet"
	"swarmjet/internal/domain"
)

const logLimit = 4 << 20

type Runner func(context.Context, []string, io.Writer) error

type Build struct {
	Reference  string     `json:"reference,omitempty"`
	ID         string     `json:"id"`
	Tag        string     `json:"tag"`
	ImageID    string     `json:"imageId,omitempty"`
	Status     string     `json:"status"`
	Source     string     `json:"source"`
	Config     Config     `json:"config"`
	CreatedAt  time.Time  `json:"createdAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Error      string     `json:"error,omitempty"`
}

type Active struct {
	BuildID   string    `json:"buildId"`
	Tag       string    `json:"tag"`
	ImageID   string    `json:"imageId"`
	AppliedAt time.Time `json:"appliedAt"`
}

type State struct {
	Config Config  `json:"config"`
	Builds []Build `json:"builds"`
}

type Manager struct {
	Resolve         func(context.Context, *Config) (Prepared, error)
	ResolvePlatform func(context.Context, string) (Prepared, string, error)
	SaveState       func(State) error

	mu      sync.Mutex
	dir     string
	run     Runner
	state   State
	cancel  context.CancelFunc
	running string
}

var platformTag = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// BuildPlatform uses the same serialized BuildKit queue and durable history as
// Agent images, while leaving the Agent build recipe untouched.
func (m *Manager) BuildPlatform(tag, sourceDir string) (Build, error) {
	if !platformTag.MatchString(tag) {
		return Build{}, errors.New("平台镜像版本标签无效")
	}
	if m.ResolvePlatform == nil || sourceDir == "" {
		return Build{}, errors.New("平台镜像构建服务未配置")
	}
	sourceDir, err := filepath.Abs(sourceDir)
	if err != nil {
		return Build{}, err
	}
	if _, err = os.Stat(filepath.Join(sourceDir, "deploy/Dockerfile")); err != nil {
		return Build{}, errors.New("平台源码或 Dockerfile 不可用")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != "" {
		return Build{}, errors.New("已有镜像正在构建，请等待完成或取消")
	}
	resolveCtx, stop := context.WithTimeout(context.Background(), 45*time.Second)
	prepared, repository, err := m.ResolvePlatform(resolveCtx, tag)
	stop()
	if err != nil {
		return Build{}, err
	}
	if err = ValidateReference(repository); err != nil {
		return Build{}, err
	}
	id, err := newID()
	if err != nil {
		return Build{}, err
	}
	buildDir := filepath.Join(m.dir, "builds", id)
	if err = os.MkdirAll(buildDir, 0700); err != nil {
		return Build{}, err
	}
	b := Build{ID: id, Tag: repository + ":" + tag, Status: "building", Source: "platform", Config: Config{Repository: repository, Platform: "linux/" + runtime.GOARCH}, CreatedAt: time.Now().UTC()}
	m.state.Builds = append([]Build{b}, m.state.Builds...)
	if err = m.save(); err != nil {
		m.state.Builds = m.state.Builds[1:]
		return Build{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	m.cancel, m.running = cancel, id
	go m.build(context.WithValue(ctx, preparedKey{}, prepared), cancel, b, sourceDir)
	return b, nil
}

func root(dataDir string) string { return filepath.Join(dataDir, "image-manager") }

func readJSON(path string, out any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 4<<20 {
		return errors.New("image manager metadata exceeds limit")
	}
	return json.Unmarshal(b, out)
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func ReadActive(dataDir string) (*Active, error) {
	if dataDir == "" {
		return nil, nil
	}
	var active Active
	if err := readJSON(filepath.Join(root(dataDir), "active.json"), &active); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if !validImageID(active.ImageID) {
		return nil, errors.New("active image metadata is invalid")
	}
	return &active, nil
}

func CurrentImage(dataDir, fallback string) (string, error) {
	active, err := ReadActive(dataDir)
	if err != nil {
		return "", err
	}
	if active == nil {
		return fallback, nil
	}
	return active.ImageID, nil
}

// ApplyToProject is used only before task creation. Execution never re-resolves
// a task's stored image, including its later review/rework phases.
func ApplyToProject(dataDir string, p domain.Project) (domain.Project, error) {
	active, err := ReadActive(dataDir)
	if err != nil {
		return p, err
	}
	if active != nil {
		p.Development.Image = active.ImageID
		p.Review.Image = active.ImageID
	}
	return p, nil
}

func New(dataDir string, runner Runner) (*Manager, error) {
	if dataDir == "" {
		return nil, errors.New("image manager data directory required")
	}
	if runner == nil {
		runner = docker
	}
	m := &Manager{dir: root(dataDir), run: runner, state: State{Config: DefaultConfig(), Builds: []Build{}}}
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(m.dir, "state.json"), &m.state); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	changed := false
	for i := range m.state.Builds {
		b := &m.state.Builds[i]
		if b.Status == "building" || b.Status == "cancelling" {
			now := time.Now().UTC()
			b.Status = "interrupted"
			b.FinishedAt = &now
			b.Error = "Worker 重启中断了构建，请重新构建。"
			changed = true
		}
	}
	if changed {
		if err := m.save(); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Manager) save() error {
	if m.SaveState != nil {
		return m.SaveState(m.state)
	}
	return writeJSON(filepath.Join(m.dir, "state.json"), m.state)
}

// UseStateStore switches metadata ownership to the control-plane database.
func (m *Manager) UseStateStore(load func(*State) error, save func(State) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e := load(&m.state); e != nil {
		return e
	}
	for i := range m.state.Builds {
		b := &m.state.Builds[i]
		if b.Status == "building" || b.Status == "cancelling" {
			b.Status = "interrupted"
			b.Error = "构建服务重启，请重新构建"
			now := time.Now().UTC()
			b.FinishedAt = &now
		}
	}
	m.SaveState = save
	return m.save()
}

func (m *Manager) Snapshot() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result State
	b, _ := json.Marshal(m.state)
	_ = json.Unmarshal(b, &result)
	return result
}

func (m *Manager) SaveConfig(c Config) error {
	if err := c.Validate(false); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	c = c.normalized()
	old := m.state.Config
	m.state.Config = c
	if err := m.save(); err != nil {
		m.state.Config = old
		return err
	}
	return nil
}

func newID() (string, error) {
	b := make([]byte, 12)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}

func (m *Manager) Build(c Config) (Build, error) {
	if err := c.Validate(true); err != nil {
		return Build{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != "" {
		return Build{}, errors.New("已有镜像正在构建，请等待完成或取消")
	}
	prepared := Prepared{}
	if m.Resolve != nil {
		var e error
		resolveCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		prepared, e = m.Resolve(resolveCtx, &c)
		stop()
		if e != nil {
			return Build{}, e
		}
	}

	if err := c.Validate(true); err != nil {
		return Build{}, err
	}
	c = c.normalized()
	id, err := newID()
	if err != nil {
		return Build{}, err
	}
	contextDir := filepath.Join(m.dir, "builds", id, "context")
	for _, name := range []string{"deploy/Dockerfile.agent", "scripts/install-agent-clis.sh", "scripts/askpass.sh"} {
		data, err := assets.AgentImageAssets.ReadFile(name)
		if err != nil {
			return Build{}, err
		}
		path := filepath.Join(contextDir, name)
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return Build{}, err
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			return Build{}, err
		}
	}
	b := Build{ID: id, Tag: "swarmjet-agent:build-" + id, Status: "building", Source: "build", Config: c, CreatedAt: time.Now().UTC()}
	if c.Repository != "" {
		b.Tag = c.Repository + ":build-" + id
	}
	oldConfig := m.state.Config
	m.state.Config = c
	m.state.Builds = append([]Build{b}, m.state.Builds...)
	if err = m.save(); err != nil {
		m.state.Config = oldConfig
		m.state.Builds = m.state.Builds[1:]
		return Build{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	m.cancel, m.running = cancel, id
	go m.build(context.WithValue(ctx, preparedKey{}, prepared), cancel, b, contextDir)
	return b, nil
}

func (m *Manager) build(ctx context.Context, cancel context.CancelFunc, b Build, dir string) {
	defer cancel()
	path := filepath.Join(m.dir, "builds", b.ID, "build.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err == nil {
		dockerfile := "deploy/Dockerfile.agent"
		if b.Source == "platform" {
			dockerfile = "deploy/Dockerfile"
		}
		args := []string{"build", "--progress=plain", "--file", filepath.Join(dir, dockerfile), "--tag", b.Tag, "--label", "swarmjet.image-build=" + b.ID}
		if b.Source != "platform" {
			args = append(args, "--label", "swarmjet.agent-clis="+strings.Join(b.Config.Agents, ","))
		}
		if b.Config.Repository != "" {
			args = append(args, "--metadata-file", filepath.Join(m.dir, "builds", b.ID, "metadata.json"))
		}
		if b.Config.Platform != "" {
			args = append(args, "--platform", b.Config.Platform)
		}
		values := map[string]string{}
		if b.Source != "platform" {
			values = b.Config.args()
		}
		keys := []string{}
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			args = append(args, "--build-arg", key+"="+values[key])
		}
		args = append(args, dir)
		prepared, _ := ctx.Value(preparedKey{}).(Prepared)
		redacted := newSecretWriter(&limitedLog{file: f}, prepared)
		err = m.run(ctx, args, redacted)
		if closeErr := redacted.Close(); err == nil {
			err = closeErr
		}
		_ = f.Close()
	}
	imageID, reference := "", ""
	if err == nil {
		if b.Config.Repository != "" {
			reference, err = readPublished(filepath.Join(m.dir, "builds", b.ID, "metadata.json"), b.Config.Repository)
		} else {
			imageID, err = m.inspect(ctx, b.Tag)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.state.Builds {
		if m.state.Builds[i].ID != b.ID {
			continue
		}
		result := &m.state.Builds[i]
		now := time.Now().UTC()
		result.FinishedAt = &now
		switch {
		case ctx.Err() != nil:
			result.Status = "cancelled"
			result.Error = "构建已取消。"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				result.Status = "failed"
				result.Error = "构建超过 45 分钟，请检查软件源和网络。"
			}
		case err != nil:
			result.Status = "failed"
			result.Error = "镜像构建失败，请查看构建日志。"
		default:
			result.Status = "succeeded"
			result.ImageID = imageID
			result.Reference = reference
		}
		break
	}
	m.cancel = nil
	m.running = ""
	// A failed metadata write cannot publish a new active image. On restart the
	// old 'building' entry becomes interrupted and can safely be rebuilt.
	if err := m.save(); err != nil {
		for i := range m.state.Builds {
			if m.state.Builds[i].ID == b.ID {
				m.state.Builds[i].Status = "failed"
				m.state.Builds[i].Error = "构建状态无法保存，请检查磁盘空间或目录权限。"
			}
		}
	}
}

type limitedLog struct {
	file    *os.File
	written int
}

func (w *limitedLog) Write(b []byte) (int, error) {
	n := len(b)
	if w.written < logLimit {
		remaining := logLimit - w.written
		if len(b) > remaining {
			b = b[:remaining]
		}
		count, err := w.file.Write(b)
		w.written += count
		if err != nil {
			return count, err
		}
		if w.written == logLimit {
			_, _ = w.file.WriteString("\n[日志达到 4 MiB，后续输出省略]\n")
		}
	}
	return n, nil
}

func validImageID(id string) bool {
	if !strings.HasPrefix(id, "sha256:") || len(id) != 71 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "sha256:"))
	return err == nil
}

func (m *Manager) inspect(ctx context.Context, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := m.run(ctx, []string{"image", "inspect", "--format", "{{.Id}}", ref}, &out); err != nil {
		return "", errors.New("本机 Docker 中没有可用的该镜像，请先构建或拉取")
	}
	id := strings.TrimSpace(out.String())
	if !validImageID(id) {
		return "", errors.New("镜像摘要无效")
	}
	return id, nil
}

func (m *Manager) Import(ctx context.Context, ref string) (Build, error) {
	if err := ValidateReference(ref); err != nil {
		return Build{}, err
	}
	imageID, err := m.inspect(ctx, ref)
	if err != nil {
		return Build{}, err
	}
	id, err := newID()
	if err != nil {
		return Build{}, err
	}
	now := time.Now().UTC()
	b := Build{ID: id, Tag: ref, ImageID: imageID, Source: "existing", Status: "succeeded", CreatedAt: now, FinishedAt: &now, Config: DefaultConfig()}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Builds = append([]Build{b}, m.state.Builds...)
	if err = m.save(); err != nil {
		m.state.Builds = m.state.Builds[1:]
		return Build{}, err
	}
	return b, nil
}

func (m *Manager) Activate(ctx context.Context, id string) (Active, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, b := range m.state.Builds {
		if b.ID != id {
			continue
		}
		if b.Status != "succeeded" {
			return Active{}, errors.New("只能应用构建成功或已检查的镜像")
		}
		// Inspect by immutable ID, not by a mutable user tag.
		imageID, err := m.inspect(ctx, b.ImageID)
		if err != nil {
			return Active{}, err
		}
		active := Active{BuildID: id, Tag: b.Tag, ImageID: imageID, AppliedAt: time.Now().UTC()}
		if err = writeJSON(filepath.Join(m.dir, "active.json"), active); err != nil {
			return Active{}, err
		}
		return active, nil
	}
	return Active{}, errors.New("没有找到该镜像记录")
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running != id || m.cancel == nil {
		return errors.New("该镜像没有正在运行的构建")
	}
	for i := range m.state.Builds {
		if m.state.Builds[i].ID == id {
			m.state.Builds[i].Status = "cancelling"
		}
	}
	m.cancel()
	return m.save()
}

func (m *Manager) Logs(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	found := false
	for _, b := range m.state.Builds {
		found = found || b.ID == id
	}
	if !found {
		return "", errors.New("没有找到该构建")
	}
	f, err := os.Open(filepath.Join(m.dir, "builds", id, "build.log"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, logLimit+128))
	return string(b), err
}

func docker(ctx context.Context, args []string, out io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	for _, key := range []string{"PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH"} {
		if v := os.Getenv(key); v != "" {
			cmd.Env = append(cmd.Env, key+"="+v)
		}
	}
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker command failed: %w", err)
	}
	return nil
}
