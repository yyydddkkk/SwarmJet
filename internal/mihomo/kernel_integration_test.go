package mihomo

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
	"testing"
	"time"
)

func realKernel(t *testing.T) *Kernel {
	t.Helper()
	binary := os.Getenv("SWARMJET_MIHOMO_TEST_BINARY")
	if binary == "" {
		t.Skip("set SWARMJET_MIHOMO_TEST_BINARY to run real-core tests")
	}
	return &Kernel{Binary: binary, RuntimeDir: t.TempDir()}
}
func TestRealKernelProbeCannotBypassLoopbackProxy(t *testing.T) {
	k := realKernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	for _, route := range []string{"DIRECT", "REJECT"} {
		text := "mode: rule\nrules:\n - MATCH," + route + "\n"
		if e := k.Validate(ctx, text); e != nil {
			t.Fatal(e)
		}
		core, e := k.Start(ctx, text)
		if e != nil {
			t.Fatal(e)
		}
		result, e := core.Probe(ctx, target.URL, "")
		core.Close()
		if e != nil {
			t.Fatal(e)
		}
		if (route == "DIRECT") != result.OK {
			t.Fatalf("loopback probe bypassed policy: %s %+v", route, result)
		}
	}
}
func TestRealHY2ImportProbeAndGateway(t *testing.T) {
	k := realKernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	keyDER, e := x509.MarshalECPrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0600)
	listener, e := net.ListenPacket("udp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := listener.LocalAddr().(*net.UDPAddr).Port
	listener.Close()
	control, e := freePort()
	if e != nil {
		t.Fatal(e)
	}
	serverConfig := map[string]any{"log-level": "silent", "external-controller": fmt.Sprintf("127.0.0.1:%d", control), "secret": "fixture-controller-secret", "listeners": []any{map[string]any{"name": "hy2-test", "type": "hysteria2", "listen": "127.0.0.1", "port": port, "users": map[string]string{"fixture": "fixture-hy2-password"}, "certificate": certPath, "private-key": keyPath}}, "rules": []string{"MATCH,DIRECT"}}
	b, _ := yaml.Marshal(serverConfig)
	configPath := filepath.Join(dir, "server.yaml")
	if e = os.WriteFile(configPath, b, 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, k.Binary, "-d", dir, "-f", configPath)
	cmd.Env = kernelEnv()
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/version", control), nil)
		req.Header.Set("Authorization", "Bearer fixture-controller-secret")
		client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("HY2 fixture server did not start")
	}
	fp := sha256.Sum256(der)
	link := fmt.Sprintf("hy2://fixture-hy2-password@127.0.0.1:%d/?sni=localhost&insecure=1&pinSHA256=%s#HY2", port, hex.EncodeToString(fp[:]))
	box, _ := secretbox.New(strings.Repeat("b", 64))
	manager, e := New(t.TempDir(), "", box, k)
	if e != nil {
		t.Fatal(e)
	}
	defer manager.Close()
	revision, e := manager.Import("HY2 fixture", "hy2", link)
	if e != nil {
		t.Fatal(e)
	}
	result, e := manager.Probe(ctx, revision.ID, target.URL, "HY2")
	if e != nil || !result.OK || result.ProxyLatencyMS == nil {
		t.Fatalf("HY2 probe failed: %+v %v", result, e)
	}
	if e = manager.Apply(ctx, revision.ID); e != nil {
		t.Fatal(e)
	}
	gateway := httptest.NewServer(http.HandlerFunc(manager.Gateway))
	defer gateway.Close()
	proxyURL, _ := url.Parse(gateway.URL)
	proxyURL.User = url.UserPassword(manager.access.Username, manager.access.Password)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	resp, e := client.Get(target.URL)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal("authenticated gateway failed", resp.StatusCode)
	}
	bad, e := manager.Import("wrong auth", "hy2", strings.Replace(link, "fixture-hy2-password", "wrong-password", 1))
	if e != nil {
		t.Fatal(e)
	}
	failed, e := manager.Probe(ctx, bad.ID, target.URL, "HY2")
	if e != nil {
		t.Fatal(e)
	}
	if failed.OK {
		t.Fatal("wrong HY2 credentials unexpectedly passed")
	}
	if e = manager.Apply(ctx, bad.ID); e == nil {
		t.Fatal("failed candidate was applied")
	}
	if manager.View().Active != revision.ID || !manager.View().Running {
		t.Fatal("failed candidate interrupted live core")
	}
	resp, e = client.Get(target.URL)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal("working proxy lost after bad candidate")
	}
}
