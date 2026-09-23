// Package mihomo manages an optional proxy core independently of Agent images.
package mihomo

import (
	"encoding/base64"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const MaxConfig = 1 << 20

type Imported struct {
	YAML     string   `json:"yaml"`
	Warnings []string `json:"warnings"`
}

// Import converts share links or full YAML to a portable outbound configuration.
// Listener, controller, routing and filesystem ownership stay with this module.
func Import(format, text string) (Imported, error) {
	if len(text) == 0 || len(text) > MaxConfig {
		return Imported{}, fmt.Errorf("配置不能为空，且不能超过 1 MiB")
	}
	if format == "hy2" {
		cfg, e := parseHY2(strings.TrimSpace(text))
		if e != nil {
			return Imported{}, e
		}
		raw, e := yaml.Marshal(cfg)
		if e != nil {
			return Imported{}, fmt.Errorf("无法生成 HY2 配置")
		}
		text = string(raw)
	} else if format != "yaml" {
		return Imported{}, fmt.Errorf("请选择 HY2 链接或 YAML 格式")
	}
	var cfg map[string]any
	dec := yaml.NewDecoder(strings.NewReader(text))
	if e := dec.Decode(&cfg); e != nil {
		return Imported{}, fmt.Errorf("YAML 无效：请检查缩进、重复键及字段类型")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || len(cfg) == 0 {
		return Imported{}, fmt.Errorf("只接受一份 YAML 配置对象")
	}
	warnings := []string{}
	managed := []string{"port", "socks-port", "mixed-port", "redir-port", "tproxy-port", "allow-lan", "bind-address", "authentication", "skip-auth-prefixes", "lan-allowed-ips", "lan-disallowed-ips", "external-controller", "external-controller-tls", "external-controller-unix", "external-controller-pipe", "external-ui", "external-ui-name", "external-ui-url", "external-controller-cors", "secret", "listeners", "tunnels", "tun", "interface-name", "routing-mark", "log-level", "profile"}
	for _, key := range managed {
		if _, ok := cfg[key]; ok {
			warnings = append(warnings, key+" 由受管服务统一设置，导入值不生效")
			delete(cfg, key)
		}
	}
	if dns, ok := cfg["dns"].(map[string]any); ok {
		if _, exists := dns["listen"]; exists {
			warnings = append(warnings, "DNS 监听地址已移除；保留内核的域名解析配置")
			delete(dns, "listen")
		}
	}
	if ntp, ok := cfg["ntp"].(map[string]any); ok {
		if ntp["write-to-system"] == true {
			warnings = append(warnings, "已禁止修改宿主机时间")
		}
		ntp["write-to-system"] = false
	}
	// Preserve HTTP providers, but never interpret an imported local path on the server.
	for _, key := range []string{"proxy-providers", "rule-providers"} {
		if providers, ok := cfg[key].(map[string]any); ok {
			for _, value := range providers {
				p, ok := value.(map[string]any)
				if !ok {
					return Imported{}, fmt.Errorf("provider 必须是对象")
				}
				typ, _ := p["type"].(string)
				if typ == "file" {
					return Imported{}, fmt.Errorf("配置引用本地 provider 文件，请改为内联内容或 HTTP provider 后再导入")
				}
				if _, ok := p["path"]; ok {
					delete(p, "path")
					warnings = append(warnings, "provider 缓存路径由服务分配")
				}
				if typ == "http" {
					raw, _ := p["url"].(string)
					u, e := url.Parse(raw)
					if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
						return Imported{}, fmt.Errorf("HTTP provider URL 无效")
					}
				}
			}
		}
	}
	if e := portableFields(cfg, 0); e != nil {
		return Imported{}, e
	}
	if rules, ok := cfg["rules"].([]any); ok {
		for _, r := range rules {
			if s, ok := r.(string); ok && strings.HasPrefix(s, "PROCESS-") {
				warnings = append(warnings, "进程规则只能识别服务端进程，不能识别远程 Agent 的进程名；建议检查这些规则")
				break
			}
		}
	}
	if cfg["mode"] == nil {
		cfg["mode"] = "rule"
	}
	if cfg["rules"] == nil && cfg["mode"] != "global" {
		return Imported{}, fmt.Errorf("规则模式需要 rules；请补充 MATCH 规则或使用 global 模式")
	}
	raw, e := yaml.Marshal(cfg)
	if e != nil || len(raw) > MaxConfig {
		return Imported{}, fmt.Errorf("展开后的配置过大或格式不支持")
	}
	return Imported{YAML: string(raw), Warnings: warnings}, nil
}
func portableFields(value any, depth int) error {
	if depth > 40 {
		return fmt.Errorf("配置嵌套过深")
	}
	switch v := value.(type) {
	case map[string]any:
		for k, item := range v {
			if k == "interface-name" || k == "routing-mark" {
				return fmt.Errorf("出站配置包含本机网卡或路由标记，请移除后导入")
			}
			if k == "private-key" && v["type"] == "wireguard" {
				if s, ok := item.(string); ok {
					if b, e := base64.StdEncoding.DecodeString(s); e == nil && len(b) == 32 {
						continue
					}
				}
			}
			if k == "certificate" || k == "private-key" || k == "ca" {
				if s, ok := item.(string); ok && s != "" && !strings.Contains(s, "-----BEGIN ") {
					return fmt.Errorf("配置引用证书或密钥文件，请改为内联 PEM 内容")
				}
			}
			if e := portableFields(item, depth+1); e != nil {
				return e
			}
		}
	case []any:
		for _, item := range v {
			if e := portableFields(item, depth+1); e != nil {
				return e
			}
		}
	}
	return nil
}
func parseHY2(raw string) (map[string]any, error) {
	// net/url rejects hopping ports; parse that authority portion before URL decoding.
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || (scheme != "hy2" && scheme != "hysteria2") {
		return nil, fmt.Errorf("仅支持 hy2:// 或 hysteria2:// 链接")
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority := rest[:end]
	hostPart := authority
	if at := strings.LastIndex(hostPart, "@"); at >= 0 {
		hostPart = hostPart[at+1:]
	}
	ports := ""
	if colon := strings.LastIndex(hostPart, ":"); colon >= 0 && (!strings.HasPrefix(hostPart, "[") || colon > strings.Index(hostPart, "]")) {
		ports = hostPart[colon+1:]
	}
	port := 443
	if ports != "" {
		for _, piece := range strings.Split(ports, ",") {
			ends := strings.Split(piece, "-")
			if len(ends) > 2 {
				return nil, fmt.Errorf("HY2 端口无效")
			}
			previous := 0
			for _, part := range ends {
				n, e := strconv.Atoi(part)
				if e != nil || n < 1 || n > 65535 || n < previous {
					return nil, fmt.Errorf("HY2 端口范围无效")
				}
				previous = n
			}
			if port == 443 {
				port, _ = strconv.Atoi(ends[0])
			}
		}
		authority = authority[:len(authority)-len(ports)] + strconv.Itoa(port)
	}
	u, e := url.Parse("hysteria2://" + authority + rest[end:])
	if e != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(raw, "\r\n\x00 ") {
		return nil, fmt.Errorf("HY2 链接格式无效")
	}
	password := ""
	if u.User != nil {
		password = u.User.Username()
		if p, ok := u.User.Password(); ok {
			password += ":" + p
		}
	}
	name := u.Fragment
	if name == "" {
		name = "HY2"
	}
	if len(name) > 200 {
		return nil, fmt.Errorf("节点名称过长")
	}
	if name == "SWARMJET-PROXY" || name == "DIRECT" || name == "GLOBAL" || name == "REJECT" {
		name = "HY2-" + name
	}
	proxy := map[string]any{"name": name, "type": "hysteria2", "server": u.Hostname(), "port": port, "password": password, "skip-cert-verify": false}
	if strings.ContainsAny(ports, ",-") {
		proxy["ports"] = ports
	}
	query, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		return nil, fmt.Errorf("HY2 查询参数无效")
	}
	for k, values := range query {
		if len(values) != 1 {
			return nil, fmt.Errorf("HY2 链接不能重复指定参数")
		}
		v := values[0]
		switch k {
		case "sni", "obfs-password", "up", "down":
			proxy[k] = v
		case "obfs":
			if v != "" && v != "salamander" && v != "gecko" {
				return nil, fmt.Errorf("不支持的 HY2 混淆类型")
			}
			proxy[k] = v
		case "insecure":
			if v != "0" && v != "1" && v != "true" && v != "false" {
				return nil, fmt.Errorf("insecure 应为 0 或 1")
			}
			proxy["skip-cert-verify"] = v == "1" || v == "true"
		case "pinSHA256":
			v = strings.ReplaceAll(v, ":", "")
			if !regexp.MustCompile(`^[a-fA-F0-9]{64}$`).MatchString(v) {
				return nil, fmt.Errorf("pinSHA256 应为完整 SHA-256 指纹")
			}
			proxy["fingerprint"] = strings.ToLower(v)
		case "ech":
			proxy["ech-opts"] = map[string]any{"enable": true, "config": v}
		case "alpn":
			proxy["alpn"] = strings.Split(v, ",")
		default:
			return nil, fmt.Errorf("链接包含未支持的扩展参数，请使用 YAML 导入")
		}
	}
	return map[string]any{"mode": "rule", "proxies": []any{proxy}, "proxy-groups": []any{map[string]any{"name": "SWARMJET-PROXY", "type": "select", "proxies": []string{name}}}, "rules": []string{"MATCH,SWARMJET-PROXY"}}, nil
}

// render adds only module-owned listeners, auth and cache paths to an already imported revision.
func render(text string, proxyPort, controllerPort int, secret string) ([]byte, error) {
	var cfg map[string]any
	if e := yaml.Unmarshal([]byte(text), &cfg); e != nil {
		return nil, e
	}
	cfg["mixed-port"] = proxyPort
	cfg["port"] = 0
	cfg["socks-port"] = 0
	cfg["redir-port"] = 0
	cfg["tproxy-port"] = 0
	cfg["allow-lan"] = false
	cfg["bind-address"] = "127.0.0.1"
	cfg["external-controller"] = fmt.Sprintf("127.0.0.1:%d", controllerPort)
	cfg["secret"] = secret
	cfg["log-level"] = "silent"
	cfg["tun"] = map[string]any{"enable": false}
	cfg["profile"] = map[string]any{"store-selected": false, "store-fake-ip": false}
	for _, key := range []string{"proxy-providers", "rule-providers"} {
		if providers, ok := cfg[key].(map[string]any); ok {
			i := 0
			for _, value := range providers {
				p := value.(map[string]any)
				if p["type"] == "http" {
					p["path"] = fmt.Sprintf("./cache/%s-%d.yaml", key, i)
				}
				i++
			}
		}
	}
	return yaml.Marshal(cfg)
}
