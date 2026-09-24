package domain

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// NetworkChoice distinguishes inheritance from an explicit direct connection.
type NetworkChoice struct {
	Mode string `json:"mode"`
	ID   string `json:"id,omitempty"`
}
type NetworkVersion struct {
	ID      string `json:"id,omitempty"`
	Version int    `json:"version,omitempty"`
}
type NetworkProfile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     int    `json:"version"`
	HTTPProxy   string `json:"httpProxy"`
	HTTPSProxy  string `json:"httpsProxy"`
	NoProxy     string `json:"noProxy"`
	Username    string `json:"username,omitempty"`
	HasPassword bool   `json:"hasPassword"`
	Revoked     bool   `json:"revoked"`
}

func (p NetworkProfile) Validate() error {
	if !IDPattern.MatchString(p.ID) || strings.TrimSpace(p.Name) == "" || len(p.Name) > 200 || len(p.Username) > 512 {
		return fmt.Errorf("网络配置需要有效 ID 和名称")
	}
	for _, raw := range []string{p.HTTPProxy, p.HTTPSProxy} {
		if raw == "" {
			continue
		}
		u, e := url.Parse(raw)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(raw, "\r\n\x00") {
			return fmt.Errorf("代理必须为无内嵌认证信息的 HTTP(S) 地址")
		}
	}
	if len(p.NoProxy) > 4096 || strings.ContainsAny(p.NoProxy, "\r\n\x00") {
		return fmt.Errorf("NO_PROXY 无效")
	}
	return nil
}
func (c NetworkChoice) Validate() error {
	switch c.Mode {
	case "", "default", "direct":
		if c.ID != "" {
			return fmt.Errorf("仅指定配置模式可设置网络 ID")
		}
	case "profile":
		if !IDPattern.MatchString(c.ID) {
			return fmt.Errorf("请选择网络配置")
		}
	default:
		return fmt.Errorf("无效网络配置模式")
	}
	return nil
}

var RepositoryDigest = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*@sha256:[a-f0-9]{64}$`)

type NodeConfig struct {
	Images    []string      `json:"images,omitempty"`
	MemoryMB  int           `json:"memoryMB"`
	CPUs      float64       `json:"cpus"`
	PidsLimit int           `json:"pidsLimit"`
	Version   int           `json:"version"`
	Name      string        `json:"name"`
	Enabled   bool          `json:"enabled"`
	Archived  bool          `json:"archived"`
	Capacity  int           `json:"capacity"`
	Network   NetworkChoice `json:"network"`
	Image     string        `json:"image"`
}

func (c NodeConfig) Validate() error {
	if len(c.Images) > 20 {
		return fmt.Errorf("最多预加载 20 个额外镜像")
	}
	for _, ref := range c.Images {
		if !RepositoryDigest.MatchString(ref) {
			return fmt.Errorf("额外镜像必须使用仓库摘要")
		}
	}
	if c.MemoryMB < 0 || c.MemoryMB > 1048576 || c.CPUs < 0 || c.CPUs > 1024 || c.PidsLimit < 0 || c.PidsLimit > 65536 {
		return fmt.Errorf("资源限制无效")
	}
	if c.Capacity < 1 || c.Capacity > 128 {
		return fmt.Errorf("节点容量应在 1–128 之间")
	}
	if c.Image != "" && !RepositoryDigest.MatchString(c.Image) {
		return fmt.Errorf("节点镜像必须是仓库地址@sha256:摘要")
	}
	return c.Network.Validate()
}

type ReadyImage struct {
	Reference    string   `json:"reference"`
	Architecture string   `json:"architecture"`
	Agents       []string `json:"agents"`
	Status       string   `json:"status"`
	Error        string   `json:"error,omitempty"`
}
type NodeReadiness struct {
	PlatformImage string         `json:"platformImage,omitempty"`
	Images        []ReadyImage   `json:"images,omitempty"`
	DockerNetwork string         `json:"dockerNetwork,omitempty"`
	ConfigVersion int            `json:"configVersion"`
	Network       NetworkVersion `json:"network"`
	Architecture  string         `json:"architecture"`
	Image         string         `json:"image"`
	Agents        []string       `json:"agents"`
	Status        string         `json:"status"`
	Error         string         `json:"error,omitempty"`
}
type NodeSelection struct {
	Mode string   `json:"mode"`
	IDs  []string `json:"ids,omitempty"`
}

func (s NodeSelection) Validate() error {
	if s.Mode != "" && s.Mode != "auto" && s.Mode != "selected" {
		return fmt.Errorf("无效节点选择模式")
	}
	if s.Mode == "selected" && len(s.IDs) == 0 {
		return fmt.Errorf("请至少选择一个节点")
	}
	seen := map[string]bool{}
	for _, id := range s.IDs {
		if !IDPattern.MatchString(id) || seen[id] {
			return fmt.Errorf("无效或重复节点 ID")
		}
		seen[id] = true
	}
	return nil
}
func (s NodeSelection) Allows(id string) bool {
	if s.Mode != "selected" {
		return true
	}
	for _, v := range s.IDs {
		if v == id {
			return true
		}
	}
	return false
}
