package mihomo

import (
	"gopkg.in/yaml.v3"
	"strings"
	"testing"
)

func TestHY2ImportPreservesCredentialsAndTLS(t *testing.T) {
	out, e := Import("hy2", "hy2://user:p%40ss@[::1]:443,5000-5010/?sni=example.test&obfs=salamander&obfs-password=a%26b&insecure=1#测试出口")
	if e != nil {
		t.Fatal(e)
	}
	var cfg map[string]any
	if e = yaml.Unmarshal([]byte(out.YAML), &cfg); e != nil {
		t.Fatal(e)
	}
	p := cfg["proxies"].([]any)[0].(map[string]any)
	if p["password"] != "user:p@ss" || p["server"] != "::1" || p["ports"] != "443,5000-5010" || p["obfs-password"] != "a&b" || p["skip-cert-verify"] != true {
		t.Fatal("HY2 fields were not preserved")
	}
}
func TestHY2DefaultsAndRejectMalformed(t *testing.T) {
	out, e := Import("hy2", "hysteria2://encoded%3Apassword@server.test#edge")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out.YAML, "port: 443") || !strings.Contains(out.YAML, "skip-cert-verify: false") {
		t.Fatal(out.YAML)
	}
	for _, raw := range []string{"https://a", "hy2://a:70000", "hy2://a:500-400", "hy2://a?insecure=maybe", "hy2://a?sni=x&sni=y", "hy2://a?unknown=secret", "hy2://a?pinSHA256=short"} {
		if _, e = Import("hy2", raw); e == nil {
			t.Fatal("accepted malformed link")
		}
	}
}
func TestYAMLPortabilityAndControlledIngress(t *testing.T) {
	out, e := Import("yaml", `mixed-port: 9999
secret: private-controller
external-controller: 0.0.0.0:9090
tun:
  enable: true
proxy-providers:
  sample:
    type: http
    url: https://example.test/provider?token=private-provider
    path: /home/me/provider.yaml
proxy-groups:
  - name: selected
    type: select
    use: [sample]
rules: [MATCH,selected]
`)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(out.YAML, "private-controller") || strings.Contains(out.YAML, "/home/me") || len(out.Warnings) < 4 {
		t.Fatal("managed fields were not stripped")
	}
	if !strings.Contains(out.YAML, "private-provider") {
		t.Fatal("provider credentials lost")
	}
	raw, e := render(out.YAML, 17890, 19090, "owned-secret")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(raw), "127.0.0.1:19090") || !strings.Contains(string(raw), "enable: false") {
		t.Fatal("runtime listener ownership lost")
	}
}
func TestYAMLRejectsLocalFilesAndMultipleDocuments(t *testing.T) {
	for _, raw := range []string{"rules: [MATCH,DIRECT]\n---\nrules: []", "proxy-providers:\n  a:\n    type: file\n    path: /secret\nrules: [MATCH,DIRECT]", "proxies:\n - name: a\n   private-key: /root/private.key\nrules: [MATCH,DIRECT]", "rules: []\nrules: []"} {
		if _, e := Import("yaml", raw); e == nil {
			t.Fatal("accepted unsafe/ambiguous configuration")
		}
	}
}
