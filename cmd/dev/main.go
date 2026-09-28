// Command dev runs the Linux native development stack and reloads Go processes
// when their source changes. Docker still supplies the existing dependencies.
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var root, state string

func main() {
	var err error
	root, err = os.Getwd()
	if err != nil {
		fatal(err)
	}
	state = filepath.Join(root, ".cache", "dev")
	if len(os.Args) != 2 {
		fatal(errors.New("usage: scripts/dev {start|stop|restart|status}"))
	}
	switch os.Args[1] {
	case "start":
		err = start()
	case "stop":
		err = stop()
	case "restart":
		if err = stop(); err == nil {
			err = start()
		}
	case "status":
		err = status()
	case "run":
		err = supervise()
	default:
		err = errors.New("usage: scripts/dev {start|stop|restart|status}")
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func file(name string) string { return filepath.Join(state, name) }

func runningPID(name, marker string) int {
	data, err := os.ReadFile(file(name))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	args, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || !strings.Contains(string(args), marker) || !strings.Contains(string(args), "\x00run") {
		return 0
	}
	return pid
}

func currentPID() int { return runningPID("dev.pid", "/.cache/dev/devctl") }
func legacyPID() int  { return runningPID("supervisor.pid", "/scripts/dev.py") }

func stopPID(pid int, marker string) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	for i := 0; i < 100; i++ {
		args, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil || !strings.Contains(string(args), marker) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("development supervisor %d did not stop", pid)
}

func stop() error {
	if pid := currentPID(); pid != 0 {
		if err := stopPID(pid, "/.cache/dev/devctl"); err != nil {
			return err
		}
		fmt.Println("Native development processes stopped; dependencies/data retained.")
		return nil
	}
	if pid := legacyPID(); pid != 0 {
		if err := stopPID(pid, "/scripts/dev.py"); err != nil {
			return err
		}
		fmt.Println("Previous Python development supervisor stopped; dependencies/data retained.")
		return nil
	}
	fmt.Println("Native development processes are stopped.")
	return nil
}

func start() error {
	if currentPID() != 0 {
		fmt.Println("Already running; Go changes reload automatically.")
		return nil
	}
	if legacyPID() != 0 {
		if err := stop(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		return err
	}
	_ = os.Remove(file("ready.json"))
	log, err := os.OpenFile(file("supervisor.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "run")
	cmd.Dir, cmd.Stdout, cmd.Stderr = root, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for i := 0; i < 240; i++ {
		if data, err := os.ReadFile(file("ready.json")); err == nil {
			var ready struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(data, &ready) == nil && ready.URL != "" {
				fmt.Println("Native development ready: " + ready.URL)
				fmt.Println("Go source changes now rebuild and restart the backend automatically.")
				return nil
			}
		}
		select {
		case <-done:
			return errors.New("startup failed; inspect .cache/dev/supervisor.log")
		case <-time.After(500 * time.Millisecond):
		}
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	return errors.New("startup timed out; inspect .cache/dev/supervisor.log")
}

func status() error {
	if currentPID() == 0 {
		if legacyPID() != 0 {
			fmt.Println("Previous Python development supervisor is running; scripts/dev start will replace it.")
		} else {
			fmt.Println("Stopped or starting")
		}
		return nil
	}
	data, err := os.ReadFile(file("ready.json"))
	if err != nil {
		fmt.Println("Starting")
		return nil
	}
	fmt.Println(string(data))
	return nil
}

func command(env map[string]string, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.Dir = root
	cmd.Env = make([]string, 0, len(env))
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd
}

func run(env map[string]string, name string, args ...string) ([]byte, error) {
	cmd := command(env, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %s failed: %w: %s", name, strings.Join(args, " "), err, out)
	}
	return out, nil
}

func setDefault(env map[string]string, key, value string) {
	if env[key] == "" {
		env[key] = value
	}
}

func environment() (map[string]string, error) {
	env := map[string]string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	loader := `import {loadEnv} from "./web/node_modules/vite/dist/node/index.js"; process.stdout.write(JSON.stringify(loadEnv("development",process.cwd()+"/deploy","")))`
	out, err := run(env, "node", "--input-type=module", "-e", loader)
	if err != nil {
		return nil, err
	}
	var loaded map[string]string
	if err := json.Unmarshal(out, &loaded); err != nil {
		return nil, err
	}
	for key, value := range loaded {
		env[key] = value
	}
	setDefault(env, "DATA_DIR", filepath.Join(root, "data"))
	setDefault(env, "TEMPORAL_NAMESPACE", "swarmjet")
	setDefault(env, "TEMPORAL_ADDRESS", "temporal:7233")
	if env["RABBITMQ_PASSWORD"] == "" {
		secret, err := persistentSecret(file("rabbitmq-password"))
		if err != nil {
			return nil, err
		}
		env["RABBITMQ_PASSWORD"] = secret
	}
	setDefault(env, "RABBITMQ_URL", "amqp://swarmjet:"+url.QueryEscape(env["RABBITMQ_PASSWORD"])+"@rabbitmq:5672/")
	setDefault(env, "DEV_WEB_PORT", "5273")
	env["PUBLIC_ORIGIN"] = "http://localhost:" + env["DEV_WEB_PORT"]
	apiHost := value(env, "DEV_API_HOST", "127.0.0.1")
	apiIP := net.ParseIP(apiHost)
	if apiIP == nil || apiIP.To4() == nil || !(apiIP.IsPrivate() || apiIP.IsLoopback()) {
		return nil, errors.New("DEV_API_HOST 必须是本机回环或内网 IPv4 地址")
	}
	env["LISTEN_ADDRESS"] = net.JoinHostPort(apiHost, value(env, "DEV_API_PORT", "8181"))
	env["SWARMJET_HOST_LISTEN_ADDRESS"] = env["LISTEN_ADDRESS"]
	env["DEV_API_URL"] = "http://" + env["LISTEN_ADDRESS"]
	env["SWARMJET_API_ONLY"] = "1"
	env["RUNTIME_ADMIN_LISTEN"] = "127.0.0.1:" + value(env, "DEV_RUNTIME_PORT", "8191")
	env["RUNTIME_ADMIN_URL"] = "http://" + env["RUNTIME_ADMIN_LISTEN"]
	if env["NODE_SECRET"] == "" {
		secret, err := persistentSecret(file("node-secret"))
		if err != nil {
			return nil, err
		}
		env["NODE_SECRET"] = secret
	}
	env["BUILDER_LISTEN_ADDRESS"] = "127.0.0.1:" + value(env, "DEV_BUILDER_PORT", "8193")
	env["BUILDER_ADMIN_URL"] = "http://" + env["BUILDER_LISTEN_ADDRESS"]
	env["BUILDER_PROXY_LISTEN"] = "127.0.0.1:" + value(env, "DEV_BUILDER_PROXY_PORT", "8194")
	env["DEV_BUILDKIT_DIR"] = file("buildkit")
	env["DEV_BUILDKIT_GID"] = strconv.Itoa(os.Getgid())
	env["BUILDKIT_HOST"] = "unix://" + file("buildkit/buildkitd.sock")
	env["SWARMJET_PLATFORM_SOURCE_DIR"] = root
	if env["MIHOMO_ENABLED"] == "1" {
		secret, err := persistentSecret(file("mihomo-secret"))
		if err != nil {
			return nil, err
		}
		setDefault(env, "MIHOMO_ADMIN_SECRET", secret)
		env["MIHOMO_ADMIN_LISTEN"] = "127.0.0.1:" + value(env, "DEV_MIHOMO_PORT", "8195")
		env["MIHOMO_ADMIN_URL"] = "http://" + env["MIHOMO_ADMIN_LISTEN"]
		setDefault(env, "MIHOMO_PROXY_LISTEN", "127.0.0.1:"+value(env, "DEV_MIHOMO_PROXY_PORT", "17890"))
		setDefault(env, "MIHOMO_PUBLIC_URL", "http://"+env["MIHOMO_PROXY_LISTEN"])
		setDefault(env, "MIHOMO_DATA_DIR", filepath.Join(env["DATA_DIR"], "mihomo"))
		setDefault(env, "MIHOMO_RUNTIME_DIR", file("mihomo-runtime"))
	}
	env["SECURE_COOKIES"] = "false"
	env["SWARMJET_GIT_ASKPASS"] = filepath.Join(root, "scripts", "askpass.sh")
	setDefault(env, "GIT_AUTHOR_NAME", "swarmjet")
	setDefault(env, "GIT_AUTHOR_EMAIL", "swarmjet@example.invalid")
	setDefault(env, "GIT_COMMITTER_NAME", env["GIT_AUTHOR_NAME"])
	setDefault(env, "GIT_COMMITTER_EMAIL", env["GIT_AUTHOR_EMAIL"])
	return env, nil
}

func value(env map[string]string, key, fallback string) string {
	if env[key] != "" {
		return env[key]
	}
	return fallback
}

func persistentSecret(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(data)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(bytes)
	if err := os.WriteFile(path, []byte(secret), 0600); err != nil {
		return "", err
	}
	return secret, nil
}

func prepareRegistry(env map[string]string) error {
	host := env["REGISTRY_BIND_HOST"]
	if host == "" {
		out, err := run(env, "ip", "-4", "route", "show", "table", "main", "default")
		if err != nil {
			return err
		}
		fields := strings.Fields(string(out))
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == "dev" {
				iface, err := net.InterfaceByName(fields[i+1])
				if err != nil {
					return err
				}
				addrs, err := iface.Addrs()
				if err != nil {
					return err
				}
				for _, addr := range addrs {
					if ip, _, err := net.ParseCIDR(addr.String()); err == nil && ip.To4() != nil && ip.IsPrivate() {
						host = ip.String()
						break
					}
				}
				break
			}
		}
	}
	if host == "" {
		return errors.New("无法确定局域网 IPv4 地址；请在 deploy/.env.development 设置 REGISTRY_BIND_HOST")
	}
	port := value(env, "REGISTRY_PORT", "5443")
	configRoot := filepath.Join(env["DATA_DIR"], "registry-config")
	envFile := file(".env.registry")
	if _, err := run(env, "python3", filepath.Join(root, "deploy/registry_setup.py"), "--root", configRoot, "--host", host, "--port", port, "--env-file", envFile); err != nil {
		return err
	}
	data, err := os.ReadFile(envFile)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if key, val, ok := strings.Cut(line, "="); ok {
			env[key] = val
		}
	}
	env["SWARMJET_BUILTIN_REGISTRY_ADDRESS"] = net.JoinHostPort(host, port)
	return nil
}

func compose(env map[string]string, args ...string) ([]byte, error) {
	return run(env, "docker", append([]string{"compose", "-f", filepath.Join(root, "deploy", "compose.yaml")}, args...)...)
}

func serviceIP(env map[string]string, service string) (string, error) {
	out, err := compose(env, "ps", "-q", service)
	if err != nil {
		return "", err
	}
	ids := strings.Fields(string(out))
	if len(ids) != 1 {
		return "", fmt.Errorf("existing %s service is not running", service)
	}
	out, err = run(env, "docker", "inspect", ids[0])
	if err != nil {
		return "", err
	}
	var info []struct {
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string } `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return "", err
	}
	if len(info) != 1 {
		return "", fmt.Errorf("unexpected docker inspect result for %s", service)
	}
	name := value(env, "COMPOSE_PROJECT_NAME", "swarmjet") + "_default"
	ip := info[0].NetworkSettings.Networks[name].IPAddress
	if ip == "" {
		return "", fmt.Errorf("cannot resolve %s on Compose network %s", service, name)
	}
	return ip, nil
}

func resolveDependencies(env map[string]string) error {
	db, err := url.Parse(env["DATABASE_URL"])
	if err != nil {
		return err
	}
	if db.Hostname() == "postgres" {
		ip, err := serviceIP(env, "postgres")
		if err != nil {
			return err
		}
		port := db.Port()
		if port == "" {
			port = "5432"
		}
		db.Host = net.JoinHostPort(ip, port)
		env["DATABASE_URL"] = db.String()
	}
	if strings.Split(env["TEMPORAL_ADDRESS"], ":")[0] == "temporal" {
		ip, err := serviceIP(env, "temporal")
		if err != nil {
			return err
		}
		env["TEMPORAL_ADDRESS"] = net.JoinHostPort(ip, "7233")
	}
	if rabbit, err := url.Parse(env["RABBITMQ_URL"]); err != nil {
		return err
	} else if rabbit.Hostname() == "rabbitmq" {
		ip, err := serviceIP(env, "rabbitmq")
		if err != nil {
			return err
		}
		rabbit.Host = net.JoinHostPort(ip, "5672")
		env["RABBITMQ_URL"] = rabbit.String()
	}
	return nil
}

type child struct {
	cmd  *exec.Cmd
	done chan error
}

type exit struct {
	name string
	gen  int
	err  error
}

func launch(env map[string]string, name string, gen int, exits chan<- exit, binary string) (*child, error) {
	var cmd *exec.Cmd
	switch name {
	case "web":
		cmd = command(env, "node", filepath.Join(root, "web/node_modules/vite/bin/vite.js"), "--host", "127.0.0.1", "--port", env["DEV_WEB_PORT"], "--strictPort")
		cmd.Dir = filepath.Join(root, "web")
	case "node":
		nodeEnv := map[string]string{}
		for key, val := range env {
			nodeEnv[key] = val
		}
		for _, key := range []string{"DATABASE_URL", "ADMIN_PASSWORD", "SWARMJET_MASTER_KEY", "RUNTIME_ADMIN_SECRET", "MIHOMO_ADMIN_SECRET"} {
			delete(nodeEnv, key)
		}
		nodeEnv["NODE_ID"] = "local"
		nodeEnv["DATA_DIR"] = filepath.Join(env["DATA_DIR"], "local-node")
		nodeEnv["NODE_LISTEN_ADDRESS"] = "127.0.0.1:" + value(env, "DEV_NODE_PORT", "8192")
		nodeEnv["NODE_ADVERTISE_URL"] = "http://" + nodeEnv["NODE_LISTEN_ADDRESS"]
		nodeEnv["CONTROL_PLANE_URL"] = env["DEV_API_URL"]
		cmd = command(nodeEnv, binary, "node")
	default:
		mode := name
		if name == "api" {
			mode = "serve"
		}
		cmd = command(env, binary, mode)
	}
	log, err := os.OpenFile(file(name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &child{cmd: cmd, done: make(chan error, 1)}
	go func() {
		err := cmd.Wait()
		c.done <- err
		exits <- exit{name: name, gen: gen, err: err}
	}()
	return c, nil
}

func stopChild(c *child) {
	if c == nil {
		return
	}
	_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-c.done:
	case <-time.After(40 * time.Second):
		_ = syscall.Kill(-c.cmd.Process.Pid, syscall.SIGKILL)
		<-c.done
	}
}

func ready(env map[string]string, children map[string]*child) error {
	processes := map[string]int{}
	for name, c := range children {
		processes[name] = c.cmd.Process.Pid
	}
	data, err := json.Marshal(map[string]any{"url": env["PUBLIC_ORIGIN"], "processes": processes})
	if err != nil {
		return err
	}
	return os.WriteFile(file("ready.json"), data, 0600)
}

func reachable(address string) bool {
	client := http.Client{Timeout: time.Second}
	resp, err := client.Get(address)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func registryReachable(env map[string]string) bool {
	ca, err := os.ReadFile(env["REGISTRY_CA_FILE"])
	if err != nil {
		return false
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return false
	}
	if !roots.AppendCertsFromPEM(ca) {
		return false
	}
	client := http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}}
	req, err := http.NewRequest("GET", "https://"+env["SWARMJET_BUILTIN_REGISTRY_ADDRESS"]+"/v2/", nil)
	if err != nil {
		return false
	}
	req.SetBasicAuth(env["REGISTRY_PULL_USER"], env["REGISTRY_PULL_PASSWORD"])
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func build(env map[string]string, candidate string) error {
	if _, err := run(env, value(env, "GO_BINARY", "go"), "build", "-o", candidate, "./cmd/swarmjet"); err != nil {
		return err
	}
	if _, err := run(env, candidate, "doctor"); err != nil {
		return err
	}
	_, err := run(env, candidate, "migrate")
	return err
}

func inputs() (string, error) {
	var b strings.Builder
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			return fingerprint(&b, path)
		})
		if err != nil {
			return "", err
		}
	}
	for _, pattern := range []string{"*.go", "go.mod", "go.sum", "internal/store/schema.sql", "deploy/Dockerfile.agent", "scripts/install-agent-clis.sh", "scripts/askpass.sh"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return "", err
		}
		for _, path := range matches {
			if err := fingerprint(&b, path); err != nil {
				return "", err
			}
		}
	}
	return b.String(), nil
}

func fingerprint(b *strings.Builder, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	fmt.Fprintf(b, "%s:%d:%d\n", path, info.Size(), info.ModTime().UnixNano())
	return nil
}

func supervise() error {
	if err := os.MkdirAll(state, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(file("lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another development supervisor is running")
	}
	if err := os.WriteFile(file("dev.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return err
	}
	defer os.Remove(file("dev.pid"))
	defer os.Remove(file("ready.json"))
	env, err := environment()
	if err != nil {
		return err
	}
	if err := prepareRegistry(env); err != nil {
		return err
	}
	if err := os.Setenv("PATH", env["PATH"]); err != nil {
		return err
	}
	if _, err := compose(env, "up", "-d", "--wait", "rabbitmq"); err != nil {
		return err
	}
	if err := resolveDependencies(env); err != nil {
		return err
	}
	goBinary := value(env, "GO_BINARY", "go")
	if path, err := exec.LookPath(goBinary); err != nil {
		return fmt.Errorf("Go is unavailable; set GO_BINARY: %w", err)
	} else {
		env["PATH"] = filepath.Dir(path) + string(os.PathListSeparator) + env["PATH"]
	}
	for _, tool := range []string{value(env, "META_COMMAND", "meta"), "git", "docker", "node"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("missing required tool %s: %w", tool, err)
		}
	}
	data, err := filepath.Abs(env["DATA_DIR"])
	if err != nil {
		return err
	}
	env["DATA_DIR"] = data
	if err := os.MkdirAll(data, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(env["DEV_BUILDKIT_DIR"], 0700); err != nil {
		return err
	}
	binary := file("swarmjet")
	candidate := file("swarmjet.next")
	if err := build(env, candidate); err != nil {
		return err
	}
	if err := os.Rename(candidate, binary); err != nil {
		return err
	}
	if _, err := compose(env, "stop", "api", "worker"); err != nil {
		return err
	}
	if _, err := run(env, "docker", "compose", "-p", "swarmjet-registry", "-f", filepath.Join(root, "deploy/compose.registry.yaml"), "up", "-d", "--wait"); err != nil {
		return err
	}
	if _, err := run(env, "docker", "compose", "-f", filepath.Join(root, "deploy/compose.buildkit-dev.yaml"), "up", "-d"); err != nil {
		return err
	}
	for _, port := range []string{env["DEV_WEB_PORT"], value(env, "DEV_API_PORT", "8181"), value(env, "DEV_RUNTIME_PORT", "8191"), value(env, "DEV_BUILDER_PORT", "8193"), value(env, "DEV_BUILDER_PROXY_PORT", "8194"), value(env, "DEV_NODE_PORT", "8192")} {
		listener, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			return fmt.Errorf("port %s is unavailable: %w", port, err)
		}
		_ = listener.Close()
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	goNames := []string{"worker", "builder", "node", "api"}
	if env["MIHOMO_ENABLED"] == "1" {
		goNames = append(goNames, "mihomo")
	}
	children := map[string]*child{}
	exits := make(chan exit, 64)
	gen := 1
	defer func() {
		for _, c := range children {
			stopChild(c)
		}
	}()
	for _, name := range append(goNames, "web") {
		children[name], err = launch(env, name, gen, exits, binary)
		if err != nil {
			return err
		}
	}
	deadline := time.Now().Add(45 * time.Second)
	for !(reachable(env["DEV_API_URL"]+"/healthz") && reachable(env["PUBLIC_ORIGIN"]) && registryReachable(env)) {
		if time.Now().After(deadline) {
			return errors.New("startup health check timed out; inspect .cache/dev logs")
		}
		select {
		case <-ctx.Done():
			return nil
		case event := <-exits:
			return fmt.Errorf("%s exited during startup: %v", event.name, event.err)
		case <-time.After(500 * time.Millisecond):
		}
	}
	if err := ready(env, children); err != nil {
		return err
	}
	fmt.Println("Native development ready; watching Go source changes.")
	last, err := inputs()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event := <-exits:
			if (event.gen == gen || event.name == "web") && event.name != "mihomo" {
				return fmt.Errorf("%s exited: %v", event.name, event.err)
			}
		case <-ticker.C:
			current, err := inputs()
			if err != nil {
				fmt.Fprintln(os.Stderr, "watch error:", err)
				continue
			}
			if current == last {
				continue
			}
			last = current
			fmt.Println("Go source changed; rebuilding backend...")
			if err := build(env, candidate); err != nil {
				fmt.Fprintln(os.Stderr, "backend rebuild failed; previous processes remain active:", err)
				continue
			}
			gen++
			for _, name := range goNames {
				stopChild(children[name])
				delete(children, name)
			}
			if err := os.Rename(candidate, binary); err != nil {
				return err
			}
			for _, name := range goNames {
				children[name], err = launch(env, name, gen, exits, binary)
				if err != nil {
					return err
				}
			}
			if err := ready(env, children); err != nil {
				return err
			}
			fmt.Println("Backend reloaded.")
		}
	}
}
