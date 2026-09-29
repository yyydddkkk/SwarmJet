package imagebuild

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Opt in because this starts a privileged, isolated BuildKit container. It
// proves that proxy build args reach a Dockerfile RUN process, including proxy
// authentication, without using a live registry or external network service.
func TestBuildKitRunUsesAuthenticatedProxy(t *testing.T) {
	if os.Getenv("SWARMJET_BUILDKIT_PROXY_SMOKE") == "" {
		t.Skip("set SWARMJET_BUILDKIT_PROXY_SMOKE=1 to run the isolated BuildKit proxy check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	contextDir := filepath.Join(root, "context")
	outputDir := filepath.Join(root, "output")
	if err := os.Mkdir(contextDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outputDir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"Dockerfile": "FROM scratch\nCOPY fetch /fetch\nRUN [\"/fetch\"]\n",
		"fetch.go": `package main
import ("io"; "net/http"; "os")
func main() {
 r, e := http.Get("http://swarmjet-proxy-test.invalid/run-proof"); if e != nil { panic(e) }
 defer r.Body.Close()
 b, e := io.ReadAll(r.Body); if e != nil || r.StatusCode != 200 || string(b) != "RUN-via-proxy" { panic("proxy proof failed") }
 if e = os.WriteFile("/proof", b, 0644); e != nil { panic(e) }
}`,
	} {
		if err := os.WriteFile(filepath.Join(contextDir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	compile := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(contextDir, "fetch"), filepath.Join(contextDir, "fetch.go"))
	compile.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile RUN helper: %v: %s", err, out)
	}
	var requests atomic.Int32
	password := "buildkit-test-secret-5291"
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("builder:"+password))
	proxy := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != auth {
			w.Header().Set("Proxy-Authenticate", `Basic realm="buildkit-test"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		if r.URL.String() != "http://swarmjet-proxy-test.invalid/run-proof" {
			http.Error(w, "unexpected target", http.StatusNotFound)
			return
		}
		requests.Add(1)
		_, _ = io.WriteString(w, "RUN-via-proxy")
	})}
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go proxy.Serve(listener)
	defer proxy.Close()
	var network struct {
		IPAM struct{ Config []struct{ Gateway string } }
	}
	bridge, err := exec.CommandContext(ctx, "docker", "network", "inspect", "bridge", "--format", "{{json .}}").Output()
	if err != nil {
		t.Fatalf("inspect Docker bridge: %v", err)
	}
	if err := json.Unmarshal(bridge, &network); err != nil || len(network.IPAM.Config) == 0 || network.IPAM.Config[0].Gateway == "" {
		t.Fatalf("Docker bridge has no gateway: %v", err)
	}
	proxyURL := fmt.Sprintf("http://builder:%s@%s", password, net.JoinHostPort(network.IPAM.Config[0].Gateway, fmt.Sprint(listener.Addr().(*net.TCPAddr).Port)))
	container := fmt.Sprintf("sj-buildkit-proxy-test-%d", time.Now().UnixNano())
	builder := fmt.Sprintf("sj-buildkit-proxy-test-%d", time.Now().UnixNano())
	defer exec.Command("docker", "rm", "-f", container).Run()
	defer exec.Command("docker", "buildx", "rm", builder).Run()
	cmd := exec.CommandContext(ctx, "docker", "run", "-d", "--name", container, "--privileged", "-v", root+":/run/proof", "moby/buildkit:v0.23.2", "--addr", "unix:///run/proof/buildkitd.sock", "--group", fmt.Sprint(os.Getgid()))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start isolated BuildKit: %v: %s", err, out)
	}
	socket := filepath.Join(root, "buildkitd.sock")
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(100 * time.Millisecond)
	}
	cmd = exec.CommandContext(ctx, "docker", "buildx", "create", "--name", builder, "--driver", "remote", "unix://"+socket)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create remote Buildx client: %v: %s", err, out)
	}
	cmd = exec.CommandContext(ctx, "docker", "buildx", "build", "--builder", builder, "--progress=plain", "--no-cache", "--build-arg", "HTTP_PROXY", "--build-arg", "http_proxy", "--build-arg", "NO_PROXY", "--build-arg", "no_proxy", "--output", "type=local,dest="+outputDir, contextDir)
	cmd.Env = append(os.Environ(), "HTTP_PROXY="+proxyURL, "http_proxy="+proxyURL, "NO_PROXY=localhost,127.0.0.1", "no_proxy=localhost,127.0.0.1")
	log, err := cmd.CombinedOutput()
	if err != nil {
		redacted := strings.ReplaceAll(string(log), proxyURL, "[PROXY REDACTED]")
		redacted = strings.ReplaceAll(redacted, password, "[REDACTED]")
		t.Fatalf("BuildKit RUN proxy check failed: %v: %s", err, redacted)
	}
	if strings.Contains(string(log), password) || strings.Contains(string(log), proxyURL) {
		t.Fatal("proxy credential or URL appeared in BuildKit log")
	}
	proof, err := os.ReadFile(filepath.Join(outputDir, "proof"))
	if err != nil || string(proof) != "RUN-via-proxy" || requests.Load() != 1 {
		t.Fatalf("RUN proxy proof failed: proof=%q requests=%d err=%v", proof, requests.Load(), err)
	}
}
