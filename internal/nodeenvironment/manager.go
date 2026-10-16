// Package nodeenvironment prepares node images asynchronously while heartbeats
// remain responsive. The active task runtime never re-resolves these defaults.
package nodeenvironment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/agentcli"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"sync"
	"time"
)

type Desired struct {
	Config  domain.NodeConfig     `json:"config"`
	Network domain.NetworkVersion `json:"network"`
	Env     map[string]string     `json:"env"`
}
type Reply struct {
	Registered bool     `json:"registered"`
	Desired    *Desired `json:"desired,omitempty"`
}
type Manager struct {
	Command func(context.Context, []string) ([]byte, error)
	mu      sync.Mutex
	state   domain.NodeReadiness
	running bool
	cancel  context.CancelFunc
	lastTry time.Time
	root    string
	nodeID  string
	active  *Desired
	desired *Desired
}

func New(root string, id string) *Manager {
	return &Manager{root: root, nodeID: id, state: domain.NodeReadiness{Architecture: runtime.GOARCH, Status: "unconfigured", Agents: []string{}}}
}
func (m *Manager) Status() domain.NodeReadiness {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.state
	s.Agents = append([]string{}, s.Agents...)
	s.Images = append([]domain.ReadyImage{}, s.Images...)
	return s
}
func (m *Manager) Active() *Desired {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return nil
	}
	b, _ := json.Marshal(m.active)
	var d Desired
	_ = json.Unmarshal(b, &d)
	return &d
}
func (m *Manager) Desired() *Desired {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.desired == nil {
		return nil
	}
	b, _ := json.Marshal(m.desired)
	var d Desired
	_ = json.Unmarshal(b, &d)
	return &d
}
func (m *Manager) Apply(ctx context.Context, d Desired) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(d)
	var copy Desired
	_ = json.Unmarshal(b, &copy)
	m.desired = &copy
	if m.running {
		if m.cancel != nil && (m.state.ConfigVersion != d.Config.Version || m.state.Network != d.Network) {
			m.cancel()
		}
		return
	}
	if m.state.ConfigVersion == d.Config.Version && m.state.Network == d.Network && m.state.Status == "ready" {
		return
	}
	if m.state.Status == "failed" && time.Since(m.lastTry) < 30*time.Second {
		return
	}
	m.running = true
	m.lastTry = time.Now()
	m.state = domain.NodeReadiness{ConfigVersion: d.Config.Version, Network: d.Network, Architecture: runtime.GOARCH, DockerNetwork: "sj-node-" + m.nodeID, Image: d.Config.Image, Status: "pulling", Agents: []string{}}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	m.cancel = cancel
	go func() {
		defer cancel()
		images, e := m.prepare(runCtx, d)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.running = false
		m.cancel = nil
		if e != nil {
			m.state.Images = images
			m.state.Status = "failed"
			m.state.Error = e.Error()
			return
		}
		m.state.Status = "ready"
		m.state.Images = images
		m.state.Agents = images[0].Agents
		m.active = &d
	}()
}
func (m *Manager) prepare(ctx context.Context, d Desired) ([]domain.ReadyImage, error) {
	refs := append([]string{d.Config.Image}, d.Config.Images...)
	seen := map[string]bool{}
	out := []domain.ReadyImage{}
	for _, ref := range refs {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		copy := d
		copy.Config.Image = ref
		agents, e := m.prepareImage(ctx, copy)
		item := domain.ReadyImage{Reference: ref, Architecture: runtime.GOARCH, Agents: agents, Status: "ready"}
		if e != nil {
			item.Status = "failed"
			item.Error = e.Error()
		}
		out = append(out, item)
		if e != nil && (ref == d.Config.Image || ctx.Err() != nil) {
			return out, e
		}
	}
	return out, nil
}
func (m *Manager) prepareImage(ctx context.Context, d Desired) ([]string, error) {
	if !domain.ImageReference.MatchString(d.Config.Image) {
		return nil, fmt.Errorf("镜像地址无效")
	}
	run := func(args ...string) ([]byte, error) {
		if m.Command != nil {
			return m.Command(ctx, args)
		}
		cmd := exec.CommandContext(ctx, "docker", args...)
		return cmd.Output()
	}
	if _, err := run("network", "inspect", "sj-node-"+m.nodeID); err != nil {
		if _, e := run("network", "create", "--label", "swarmjet.node="+m.nodeID, "sj-node-"+m.nodeID); e != nil {
			return nil, fmt.Errorf("节点网络创建失败")
		}
	}
	// A failed pull still accepts an image already present on the node, such as
	// one built locally from Dockerfile.agent.
	if _, e := run("pull", d.Config.Image); e != nil {
		if _, local := run("image", "inspect", "--format", "{{.Id}}", d.Config.Image); local != nil {
			return nil, fmt.Errorf("镜像拉取失败：请检查镜像地址、Docker 代理及网络")
		}
	}
	raw, e := run("image", "inspect", "--format", "{{.Architecture}}", d.Config.Image)
	if e != nil || strings.TrimSpace(string(raw)) != runtime.GOARCH {
		return nil, fmt.Errorf("镜像架构与节点不匹配")
	}
	script := ""
	for _, a := range agentcli.Catalog() {
		script += "if command -v " + a.Executable + " >/dev/null 2>&1; then printf '%s\\n' '" + a.ID + "'; fi;\n"
	}
	// No CLI is executed: only executable presence is inspected in a restricted container.
	raw, e = run("run", "--rm", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=64m", "--pids-limit=32", "--entrypoint", "/bin/sh", d.Config.Image, "-c", script)
	if e != nil {
		return nil, fmt.Errorf("镜像 CLI 检测失败")
	}
	ids := []string{}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if _, ok := agentcli.Lookup(string(line)); ok {
			ids = append(ids, string(line))
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("镜像中未检测到支持的 Agent CLI")
	}
	return ids, nil
}
