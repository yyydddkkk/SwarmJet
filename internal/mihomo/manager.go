package mihomo

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"github.com/yyydddkkk/SwarmJet/internal/buildproxy"
	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
	"sync"
	"syscall"
	"time"
)

type Revision struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	Warnings  []string  `json:"warnings"`
}
type storedRevision struct {
	Revision
	Sealed string `json:"sealed"`
}
type state struct {
	Revisions    []storedRevision             `json:"revisions"`
	Active       string                       `json:"active"`
	Previous     string                       `json:"previous"`
	AccessSecret string                       `json:"accessSecret"`
	Selections   map[string]map[string]string `json:"selections"`
	LastProbe    *Probe                       `json:"lastProbe,omitempty"`
}
type View struct {
	Enabled   bool       `json:"enabled"`
	Running   bool       `json:"running"`
	Busy      bool       `json:"busy"`
	Active    string     `json:"active"`
	Previous  string     `json:"previous"`
	Revisions []Revision `json:"revisions"`
	LastProbe *Probe     `json:"lastProbe,omitempty"`
	Error     string     `json:"error,omitempty"`
	ProxyURL  string     `json:"proxyUrl"`
}
type Access struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type candidate struct {
	id       string
	instance Instance
	probe    *Probe
	at       time.Time
}
type Manager struct {
	op             sync.Mutex
	mu             sync.Mutex
	dir, publicURL string
	box            *secretbox.Box
	core           Core
	state          state
	active         Instance
	candidate      *candidate
	access         Access
	error          string
	busy           bool
	lock           *os.File
	closeOnce      sync.Once
	stop           chan struct{}
}

func New(dir, publicURL string, box *secretbox.Box, core Core) (*Manager, error) {
	if box == nil || core == nil {
		return nil, fmt.Errorf("Mihomo 配置加密与内核未配置")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	lock, e := os.OpenFile(filepath.Join(dir, "manager.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		return nil, fmt.Errorf("此 Mihomo 数据目录已由其他服务管理")
	}
	m := &Manager{dir: dir, publicURL: publicURL, box: box, core: core, lock: lock, stop: make(chan struct{}), state: state{Revisions: []storedRevision{}, Selections: map[string]map[string]string{}}}
	raw, e := os.ReadFile(filepath.Join(dir, "state.json"))
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		m.Close()
		return nil, e
	}
	if e == nil {
		if len(raw) > 32<<20 || json.Unmarshal(raw, &m.state) != nil {
			m.Close()
			return nil, fmt.Errorf("Mihomo 状态文件不可读")
		}
	}
	if m.state.Selections == nil {
		m.state.Selections = map[string]map[string]string{}
	}
	if m.state.AccessSecret == "" {
		password, e := randomID()
		if e != nil {
			m.Close()
			return nil, e
		}
		m.access = Access{Username: "swarmjet", Password: password}
		raw, _ := json.Marshal(m.access)
		m.state.AccessSecret, e = box.Seal(string(raw))
		if e != nil {
			m.Close()
			return nil, e
		}
		if e = m.save(m.state); e != nil {
			m.Close()
			return nil, e
		}
	} else {
		plain, e := box.Open(m.state.AccessSecret)
		if e != nil || json.Unmarshal([]byte(plain), &m.access) != nil {
			m.Close()
			return nil, fmt.Errorf("无法解密 Mihomo 认证配置，请检查持久主密钥")
		}
	}
	return m, nil
}
func (m *Manager) save(s state) error {
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(m.dir, ".state-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), filepath.Join(m.dir, "state.json"))
}
func (m *Manager) cloneState() state {
	raw, _ := json.Marshal(m.state)
	var s state
	_ = json.Unmarshal(raw, &s)
	return s
}
func (m *Manager) begin() error {
	if !m.op.TryLock() {
		return fmt.Errorf("代理服务正在处理其他操作，请稍后重试")
	}
	m.mu.Lock()
	m.busy = true
	m.mu.Unlock()
	return nil
}
func (m *Manager) end() { m.mu.Lock(); m.busy = false; m.mu.Unlock(); m.op.Unlock() }
func (m *Manager) View() View {
	m.mu.Lock()
	defer m.mu.Unlock()
	revs := []Revision{}
	for _, r := range m.state.Revisions {
		revs = append(revs, r.Revision)
	}
	return View{Enabled: true, Running: m.active != nil && m.active.Alive(), Busy: m.busy, Active: m.state.Active, Previous: m.state.Previous, Revisions: revs, LastProbe: m.state.LastProbe, Error: m.error, ProxyURL: m.publicURL}
}
func (m *Manager) revision(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.state.Revisions {
		if r.ID == id {
			return m.box.Open(r.Sealed)
		}
	}
	return "", fmt.Errorf("配置版本不存在")
}
func (m *Manager) Configuration(id string) (string, error) { return m.revision(id) }
func (m *Manager) Import(name, format, text string) (Revision, error) {
	if len(name) == 0 || len(name) > 200 {
		return Revision{}, fmt.Errorf("请填写 1–200 字符的配置名称")
	}
	if e := m.begin(); e != nil {
		return Revision{}, e
	}
	defer m.end()
	imported, e := Import(format, text)
	if e != nil {
		return Revision{}, e
	}
	sealed, e := m.box.Seal(imported.YAML)
	if e != nil {
		return Revision{}, e
	}
	id, e := randomID()
	if e != nil {
		return Revision{}, e
	}
	r := Revision{ID: id, Name: name, CreatedAt: time.Now().UTC(), Warnings: imported.Warnings}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.cloneState()
	next.Revisions = append([]storedRevision{{Revision: r, Sealed: sealed}}, next.Revisions...)
	if len(next.Revisions) > 20 {
		for i := len(next.Revisions) - 1; i >= 0; i-- {
			if next.Revisions[i].ID != next.Active && next.Revisions[i].ID != next.Previous && (m.candidate == nil || next.Revisions[i].ID != m.candidate.id) {
				delete(next.Selections, next.Revisions[i].ID)
				next.Revisions = append(next.Revisions[:i], next.Revisions[i+1:]...)
				break
			}
		}
	}
	if e = m.save(next); e != nil {
		return Revision{}, e
	}
	m.state = next
	return r, nil
}
func (m *Manager) Validate(ctx context.Context, id string) error {
	if e := m.begin(); e != nil {
		return e
	}
	defer m.end()
	text, e := m.revision(id)
	if e != nil {
		return e
	}
	return m.core.Validate(ctx, text)
}
func (m *Manager) prepare(ctx context.Context, id string) (Instance, error) {
	text, e := m.revision(id)
	if e != nil {
		return nil, e
	}
	if e = m.core.Validate(ctx, text); e != nil {
		return nil, e
	}
	instance, e := m.core.Start(ctx, text)
	if e != nil {
		return nil, e
	}
	m.mu.Lock()
	selection := map[string]string{}
	for k, v := range m.state.Selections[id] {
		selection[k] = v
	}
	m.mu.Unlock()
	for group, name := range selection {
		if e = instance.Select(ctx, group, name); e != nil {
			instance.Close()
			return nil, fmt.Errorf("无法恢复保存的策略组选择")
		}
	}
	return instance, nil
}
func (m *Manager) Probe(ctx context.Context, id, target, proxy string) (Probe, error) {
	if e := m.begin(); e != nil {
		return Probe{}, e
	}
	defer m.end()
	m.mu.Lock()
	old := m.candidate
	m.candidate = nil
	m.mu.Unlock()
	if old != nil {
		old.instance.Close()
	}
	instance, e := m.prepare(ctx, id)
	if e != nil {
		return Probe{}, e
	}
	result, e := instance.Probe(ctx, target, proxy)
	result.RevisionID = id
	if e != nil {
		instance.Close()
		return result, e
	}
	m.mu.Lock()
	next := m.cloneState()
	next.LastProbe = &result
	e = m.save(next)
	if e == nil {
		m.state = next
		m.candidate = &candidate{id: id, instance: instance, probe: &result, at: time.Now()}
	}
	m.mu.Unlock()
	if e != nil {
		instance.Close()
	}
	return result, e
}
func (m *Manager) Apply(ctx context.Context, id string) error {
	if e := m.begin(); e != nil {
		return e
	}
	defer m.end()
	m.mu.Lock()
	c := m.candidate
	m.mu.Unlock()
	if c == nil || c.id != id || c.probe == nil || !c.probe.OK || !c.instance.Alive() || time.Since(c.at) > 5*time.Minute {
		return fmt.Errorf("请先对这个版本完成可用性探针测试；测试结果有效期为 5 分钟")
	}
	if _, e := c.instance.Groups(ctx); e != nil {
		return fmt.Errorf("候选内核已不可用，请重新测试")
	}
	return m.commit(c)
}
func (m *Manager) commit(c *candidate) error {
	m.mu.Lock()
	next := m.cloneState()
	if next.Active != c.id {
		next.Previous = next.Active
	}
	next.Active = c.id
	if e := m.save(next); e != nil {
		m.mu.Unlock()
		return e
	}
	old := m.active
	m.state = next
	m.active = c.instance
	m.candidate = nil
	m.error = ""
	m.mu.Unlock()
	if old != nil && old != c.instance {
		old.Close()
	}
	return nil
}
func (m *Manager) Rollback(ctx context.Context, target, proxy string) error {
	// Rollback must pass the same candidate test; failure cannot evict the active core.
	m.mu.Lock()
	id := m.state.Previous
	m.mu.Unlock()
	if id == "" {
		return fmt.Errorf("没有可回滚的版本")
	}
	result, e := m.Probe(ctx, id, target, proxy)
	if e != nil {
		return e
	}
	if !result.OK {
		return fmt.Errorf("回滚版本探针未通过，当前配置保持不变")
	}
	return m.Apply(ctx, id)
}
func (m *Manager) Groups(ctx context.Context) ([]Group, error) {
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	if active == nil {
		return []Group{}, nil
	}
	return active.Groups(ctx)
}
func (m *Manager) Select(ctx context.Context, group, name string) error {
	if e := m.begin(); e != nil {
		return e
	}
	defer m.end()
	m.mu.Lock()
	active, id := m.active, m.state.Active
	m.mu.Unlock()
	if active == nil {
		return fmt.Errorf("尚未应用配置")
	}
	groups, e := active.Groups(ctx)
	if e != nil {
		return e
	}
	previous := ""
	allowed := false
	for _, g := range groups {
		if g.Name == group && g.Type == "Selector" {
			previous = g.Now
			for _, v := range g.All {
				if v == name {
					allowed = true
				}
			}
		}
	}
	if !allowed {
		return fmt.Errorf("请选择有效的手动策略组及出口")
	}
	if e = active.Select(ctx, group, name); e != nil {
		return e
	}
	m.mu.Lock()
	next := m.cloneState()
	if next.Selections[id] == nil {
		next.Selections[id] = map[string]string{}
	}
	next.Selections[id][group] = name
	e = m.save(next)
	if e == nil {
		m.state = next
	}
	c := m.candidate
	m.candidate = nil
	m.mu.Unlock()
	if c != nil {
		c.instance.Close()
	}
	if e != nil {
		_ = active.Select(ctx, group, previous)
	}
	return e
}
func (m *Manager) Gateway(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := proxyAuth(r.Header.Get("Proxy-Authorization"))
	if !ok || !equal(user, m.access.Username) || !equal(pass, m.access.Password) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="SwarmJet Mihomo"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	if active == nil || !active.Alive() {
		http.Error(w, "Mihomo has no applied healthy configuration", 503)
		return
	}
	gateway := &buildproxy.Proxy{ForceProxy: true}
	gateway.Use(map[string]string{"HTTP_PROXY": active.ProxyURL(), "HTTPS_PROXY": active.ProxyURL()})
	gateway.ServeHTTP(w, r)
}
func proxyAuth(value string) (string, string, bool) {
	r := &http.Request{Header: make(http.Header)}
	r.Header.Set("Authorization", value)
	return r.BasicAuth()
}
func equal(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}
func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		m.recover(ctx)
		select {
		case <-ctx.Done():
			return
		case <-m.stop:
			return
		case <-ticker.C:
		}
	}
}
func (m *Manager) recover(ctx context.Context) {
	if !m.op.TryLock() {
		return
	}
	defer m.op.Unlock()
	m.mu.Lock()
	expired := m.candidate != nil && time.Since(m.candidate.at) > 5*time.Minute
	var oldCandidate *candidate
	if expired {
		oldCandidate = m.candidate
		m.candidate = nil
	}
	active, id := m.active, m.state.Active
	m.mu.Unlock()
	if oldCandidate != nil {
		oldCandidate.instance.Close()
	}
	if id == "" || active != nil && active.Alive() {
		return
	}
	instance, e := m.prepare(ctx, id)
	m.mu.Lock()
	if e != nil {
		m.error = "活动配置恢复失败，请检查内核、配置数据源或回滚"
	} else {
		m.active = instance
		m.error = ""
	}
	m.mu.Unlock()
	if active != nil {
		active.Close()
	}
}
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		close(m.stop)
		m.op.Lock()
		defer m.op.Unlock()
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.active != nil {
			m.active.Close()
		}
		if m.candidate != nil {
			m.candidate.instance.Close()
		}
		if m.lock != nil {
			_ = syscall.Flock(int(m.lock.Fd()), syscall.LOCK_UN)
			_ = m.lock.Close()
		}
	})
}
