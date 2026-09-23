package mihomo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"swarmjet/internal/networkconfig"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Group struct {
	Name string   `json:"name"`
	Type string   `json:"type"`
	Now  string   `json:"now,omitempty"`
	All  []string `json:"all,omitempty"`
}
type Probe struct {
	RevisionID     string    `json:"revisionId"`
	URL            string    `json:"url"`
	Proxy          string    `json:"proxy,omitempty"`
	OK             bool      `json:"ok"`
	TransportOK    bool      `json:"transportOk"`
	Status         int       `json:"status,omitempty"`
	LatencyMS      int64     `json:"latencyMs"`
	ProxyLatencyMS *int      `json:"proxyLatencyMs,omitempty"`
	Error          string    `json:"error,omitempty"`
	At             time.Time `json:"at"`
}
type Instance interface {
	Alive() bool
	ProxyURL() string
	Groups(context.Context) ([]Group, error)
	Select(context.Context, string, string) error
	Probe(context.Context, string, string) (Probe, error)
	Close()
}
type Core interface {
	Validate(context.Context, string) error
	Start(context.Context, string) (Instance, error)
}
type Kernel struct{ Binary, RuntimeDir string }

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func freePort() (int, error) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return 0, e
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
func (k *Kernel) files(text string) (dir, path, secret string, proxy, controller int, err error) {
	if err = os.MkdirAll(k.RuntimeDir, 0700); err != nil {
		return
	}
	dir, err = os.MkdirTemp(k.RuntimeDir, "core-")
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	if proxy, err = freePort(); err != nil {
		return
	}
	for controller == 0 || controller == proxy {
		if controller, err = freePort(); err != nil {
			return
		}
	}
	if secret, err = randomID(); err != nil {
		return
	}
	var raw []byte
	raw, err = render(text, proxy, controller, secret)
	if err != nil {
		return
	}
	path = filepath.Join(dir, "config.yaml")
	err = os.WriteFile(path, raw, 0600)
	return
}
func (k *Kernel) Validate(ctx context.Context, text string) error {
	dir, path, _, _, _, e := k.files(text)
	if e != nil {
		return fmt.Errorf("无法准备内核配置目录")
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, k.Binary, "-t", "-d", dir, "-f", path)
	cmd.Env = kernelEnv()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if e = cmd.Run(); e != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("内核校验超时，请检查 provider、规则数据源和网络")
		}
		return fmt.Errorf("Mihomo 校验未通过，请检查字段、策略引用、证书及 provider 数据源")
	}
	return nil
}

// No platform secrets or ambient proxy variables enter the core process.
func kernelEnv() []string {
	out := []string{}
	for _, key := range []string{"PATH", "HOME", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if v := os.Getenv(key); v != "" {
			out = append(out, key+"="+v)
		}
	}
	return out
}

type process struct {
	cmd                            *exec.Cmd
	dir, proxy, controller, secret string
	alive                          atomic.Bool
	done                           chan struct{}
	once                           sync.Once
}

func (k *Kernel) Start(ctx context.Context, text string) (Instance, error) {
	dir, path, secret, proxy, control, e := k.files(text)
	if e != nil {
		return nil, fmt.Errorf("无法准备内核工作目录")
	}
	p := &process{dir: dir, proxy: fmt.Sprintf("http://127.0.0.1:%d", proxy), controller: fmt.Sprintf("http://127.0.0.1:%d", control), secret: secret, done: make(chan struct{})}
	p.cmd = exec.Command(k.Binary, "-d", dir, "-f", path)
	p.cmd.Env = kernelEnv()
	p.cmd.Stdout = io.Discard
	p.cmd.Stderr = io.Discard
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if e = p.cmd.Start(); e != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("无法启动 Mihomo 内核，请检查安装路径和权限")
	}
	p.alive.Store(true)
	go func() { _ = p.cmd.Wait(); p.alive.Store(false); close(p.done) }()
	startup, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		var version map[string]any
		if p.request(startup, http.MethodGet, "/version", nil, &version) == nil {
			return p, nil
		}
		select {
		case <-p.done:
			p.Close()
			return nil, fmt.Errorf("Mihomo 启动失败，请检查配置及外部数据源")
		case <-startup.Done():
			p.Close()
			return nil, fmt.Errorf("Mihomo 启动超时")
		case <-tick.C:
		}
	}
}
func (p *process) Alive() bool      { return p.alive.Load() }
func (p *process) ProxyURL() string { return p.proxy }
func (p *process) Close() {
	p.once.Do(func() {
		if p.cmd.Process != nil && p.alive.Load() {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
			select {
			case <-p.done:
			case <-time.After(2 * time.Second):
				_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
				<-p.done
			}
		}
		os.RemoveAll(p.dir)
	})
}
func (p *process) request(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return e
		}
		payload = bytes.NewReader(b)
	}
	req, e := http.NewRequestWithContext(ctx, method, p.controller+path, payload)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+p.secret)
	req.Header.Set("Content-Type", "application/json")
	c := &http.Client{Timeout: 16 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer c.CloseIdleConnections()
	resp, e := c.Do(req)
	if e != nil {
		return errors.New("内核控制接口不可达")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("内核请求失败（HTTP %d）", resp.StatusCode)
	}
	if out != nil {
		if e = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); e != nil {
			return errors.New("内核响应格式错误")
		}
	}
	return nil
}
func (p *process) Groups(ctx context.Context) ([]Group, error) {
	var response struct {
		Proxies map[string]struct {
			Type string   `json:"type"`
			Now  string   `json:"now"`
			All  []string `json:"all"`
		} `json:"proxies"`
	}
	if e := p.request(ctx, http.MethodGet, "/proxies", nil, &response); e != nil {
		return nil, e
	}
	out := []Group{}
	for name, v := range response.Proxies {
		out = append(out, Group{Name: name, Type: v.Type, Now: v.Now, All: v.All})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (p *process) Select(ctx context.Context, group, name string) error {
	return p.request(ctx, http.MethodPut, "/proxies/"+url.PathEscape(group), map[string]string{"name": name}, nil)
}
func (p *process) Probe(ctx context.Context, target, proxy string) (Probe, error) {
	result := Probe{URL: target, Proxy: proxy, At: time.Now().UTC()}
	if e := networkconfig.ValidateTarget(target); e != nil {
		return result, e
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if proxy != "" {
		q := url.Values{"url": []string{target}, "timeout": []string{"12000"}, "expected": []string{"200-399"}}
		var response struct {
			Delay int `json:"delay"`
		}
		if e := p.request(ctx, http.MethodGet, "/proxies/"+url.PathEscape(proxy)+"/delay?"+q.Encode(), nil, &response); e != nil {
			result.Error = "指定出口探针失败，请检查节点认证、UDP 连通性、DNS 和 TLS"
			return result, nil
		}
		result.ProxyLatencyMS = &response.Delay
	}
	// Explicit ProxyURL ignores ambient NO_PROXY, including localhost test targets.
	u, _ := url.Parse(p.proxy)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(u)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if e != nil {
		return result, e
	}
	start := time.Now()
	resp, e := client.Do(req)
	result.LatencyMS = time.Since(start).Milliseconds()
	if e != nil {
		result.Error = "代理请求失败或超时，请检查出口、目标地址与证书"
		return result, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	result.Status = resp.StatusCode
	result.TransportOK = true
	result.OK = resp.StatusCode >= 200 && resp.StatusCode < 400
	if !result.OK {
		result.Error = fmt.Sprintf("代理已返回响应，但目标返回 HTTP %d；不计为可用", resp.StatusCode)
	}
	return result, nil
}
