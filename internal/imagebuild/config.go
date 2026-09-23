// Package imagebuild manages local Agent image recipes, builds and activation.
// It never builds the platform itself or business repository images.
package imagebuild

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	assets "swarmjet"
	"swarmjet/internal/domain"
)

type Config struct {
	Repository     string                `json:"repository,omitempty"`
	Platform       string                `json:"platform,omitempty"`
	Network        domain.NetworkChoice  `json:"network"`
	NetworkVersion domain.NetworkVersion `json:"networkVersion,omitempty"`
	Agents         []string              `json:"agents"`
	BaseImage      string                `json:"baseImage"`
	ExtraPackages  []string              `json:"extraPackages"`
	BuildArgs      map[string]string     `json:"buildArgs"`
}

type Parameter struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Default string `json:"default"`
}

func Parameters() []Parameter {
	data, _ := assets.AgentImageAssets.ReadFile("deploy/Dockerfile.agent")
	defaults := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "ARG ") {
			key, value, ok := strings.Cut(strings.TrimPrefix(line, "ARG "), "=")
			if ok {
				defaults[key] = value
			}
		}
	}
	params := []Parameter{
		{Key: "OPENCODE_VERSION", Label: "OpenCode 版本"}, {Key: "CODEX_VERSION", Label: "Codex 版本"},
		{Key: "CLAUDE_VERSION", Label: "Claude Code 版本"}, {Key: "DSH_VERSION", Label: "DeepSeek Harness 版本"}, {Key: "DST_VERSION", Label: "dsh-TUI 版本"},
		{Key: "GLAB_VERSION", Label: "GitLab CLI 版本"}, {Key: "UV_VERSION", Label: "uv 版本"}, {Key: "PNPM_VERSION", Label: "pnpm 版本"},
		{Key: "NPM_REGISTRY", Label: "npm 软件源"}, {Key: "PYPI_INDEX", Label: "Python 软件源"}, {Key: "GLAB_RELEASE_URL", Label: "GitLab CLI 下载地址"},
	}
	for i := range params {
		params[i].Default = defaults[params[i].Key]
	}
	return params
}

func DefaultConfig() Config {
	args := map[string]string{}
	for _, p := range Parameters() {
		args[p.Key] = p.Default
	}
	data, _ := assets.AgentImageAssets.ReadFile("deploy/Dockerfile.agent")
	base := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "ARG AGENT_BASE_IMAGE=") {
			base = strings.TrimPrefix(line, "ARG AGENT_BASE_IMAGE=")
		}
	}
	return Config{Agents: []string{}, BaseImage: base, ExtraPackages: []string{}, BuildArgs: args}
}

var imageRef = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/@-]{0,254}$`)
var version = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_-]{0,79}$`)
var aptPackage = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*(?::[a-z0-9]+)?(?:=[a-zA-Z0-9.+:~_-]+)?$`)

func ValidateReference(ref string) error {
	if !imageRef.MatchString(ref) {
		return fmt.Errorf("请输入有效的 Docker 镜像名称或摘要")
	}
	return nil
}

func (c Config) Validate(forBuild bool) error {
	if err := c.Network.Validate(); err != nil {
		return err
	}
	if c.Platform != "" && c.Platform != "linux/amd64" && c.Platform != "linux/arm64" {
		return fmt.Errorf("目标架构应为 linux/amd64 或 linux/arm64")
	}
	if c.Repository != "" {
		if err := ValidateReference(c.Repository); err != nil {
			return err
		}
		if strings.Contains(c.Repository, "@") || !strings.Contains(c.Repository, "/") {
			return fmt.Errorf("请输入完整仓库路径")
		}
	}

	if err := domain.ValidateEnabledAgents(c.Agents); err != nil {
		return err
	}
	if forBuild && len(c.Agents) == 0 {
		return fmt.Errorf("请至少选择一个要安装的 Agent")
	}
	if err := ValidateReference(c.BaseImage); err != nil {
		return err
	}
	if len(c.ExtraPackages) > 100 {
		return fmt.Errorf("额外系统包最多 100 个")
	}
	for _, p := range c.ExtraPackages {
		if len(p) > 160 || !aptPackage.MatchString(p) {
			return fmt.Errorf("系统包名称无效：%q", p)
		}
	}
	allowed := map[string]bool{}
	for _, p := range Parameters() {
		allowed[p.Key] = true
	}
	for k, v := range c.BuildArgs {
		if !allowed[k] {
			return fmt.Errorf("不支持的构建参数 %q", k)
		}
		if strings.HasSuffix(k, "_VERSION") {
			if !version.MatchString(v) {
				return fmt.Errorf("%s 版本号无效", k)
			}
		} else {
			u, err := url.Parse(v)
			if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(v, "\r\n\x00 ") || len(v) > 1024 {
				return fmt.Errorf("%s 必须是无内嵌凭据的 HTTP(S) 地址", k)
			}
		}
	}
	return nil
}

func (c Config) args() map[string]string {
	args := DefaultConfig().BuildArgs
	for k, v := range c.BuildArgs {
		args[k] = v
	}
	args["AGENT_CLIS"] = strings.Join(c.Agents, ",")
	args["AGENT_BASE_IMAGE"] = c.BaseImage
	args["EXTRA_APT_PACKAGES"] = strings.Join(c.ExtraPackages, " ")
	return args
}

func (c Config) normalized() Config {
	args := DefaultConfig().BuildArgs
	for k, v := range c.BuildArgs {
		args[k] = v
	}
	c.BuildArgs = args
	c.Agents = append([]string{}, c.Agents...)
	c.ExtraPackages = append([]string{}, c.ExtraPackages...)
	return c
}
