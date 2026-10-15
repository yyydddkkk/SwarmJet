package registryconfig

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

type Config struct {
	Mode             string `json:"mode,omitempty"`
	BuiltinAvailable bool   `json:"builtinAvailable,omitempty"`
	BuiltinAddress   string `json:"builtinAddress,omitempty"`
	Address          string `json:"address"`
	Repository       string `json:"repository"`
	PushUser         string `json:"pushUser"`
	PullUser         string `json:"pullUser"`
	HasPushPassword  bool   `json:"hasPushPassword"`
	HasPullPassword  bool   `json:"hasPullPassword"`
}

func Builtin() (Config, bool) {
	address := os.Getenv("SWARMJET_BUILTIN_REGISTRY_ADDRESS")
	if address == "" {
		return Config{}, false
	}
	return Config{Mode: "builtin", BuiltinAvailable: true, BuiltinAddress: address, Address: address, Repository: "swarmjet/agent", PushUser: os.Getenv("REGISTRY_PUSH_USER"), PullUser: os.Getenv("REGISTRY_PULL_USER"), HasPushPassword: os.Getenv("REGISTRY_PUSH_PASSWORD") != "", HasPullPassword: os.Getenv("REGISTRY_PULL_PASSWORD") != ""}, true
}

type Secrets struct {
	Push string `json:"push"`
	Pull string `json:"pull"`
}
type Saved struct {
	Config Config `json:"config"`
	Secret string `json:"secret"`
}
type Access struct {
	Address  string `json:"address"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c Config) Validate() error {
	if c.Mode != "" && c.Mode != "custom" && c.Mode != "builtin" {
		return fmt.Errorf("无效镜像仓库模式")
	}
	u, e := url.Parse("https://" + c.Address)
	if e != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(c.Address, " \r\n") {
		return fmt.Errorf("仓库地址应为 host[:port]，不含协议和路径")
	}
	if !regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`).MatchString(c.Repository) || strings.Contains(c.Repository, "..") || strings.ContainsAny(c.Repository, " @:\r\n") || strings.HasPrefix(c.Repository, "/") {
		return fmt.Errorf("仓库路径无效")
	}
	return nil
}
func Load(ctx context.Context, s *store.Store, box *secretbox.Box, push bool) (Config, Access, error) {
	var v Saved
	e := s.Setting(ctx, "registry", &v)
	if e != nil {
		if e == store.ErrNotFound {
			if builtin, ok := Builtin(); ok {
				return builtin, builtinAccess(builtin.Address, push), nil
			}
		}
		return v.Config, Access{}, e
	}
	if v.Config.Mode == "builtin" {
		builtin, ok := Builtin()
		if !ok {
			return Config{}, Access{}, fmt.Errorf("内置镜像仓库未启用")
		}
		return builtin, builtinAccess(builtin.Address, push), nil
	}
	v.Config.Mode = "custom"
	v.Config.BuiltinAvailable = false
	v.Config.BuiltinAddress = ""
	if builtin, ok := Builtin(); ok {
		v.Config.BuiltinAvailable = true
		v.Config.BuiltinAddress = builtin.Address
	}
	a := Access{Address: v.Config.Address, Username: v.Config.PullUser}
	var sec Secrets
	if v.Secret != "" {
		if box == nil {
			return v.Config, a, fmt.Errorf("仓库凭据加密未配置")
		}
		raw, e := box.Open(v.Secret)
		if e != nil {
			return v.Config, a, e
		}
		if e = json.Unmarshal([]byte(raw), &sec); e != nil {
			return v.Config, a, e
		}
	}
	a.Password = sec.Pull
	if push {
		a.Username = v.Config.PushUser
		a.Password = sec.Push
	}
	return v.Config, a, nil
}

func builtinAccess(address string, push bool) Access {
	if push {
		return Access{Address: address, Username: os.Getenv("REGISTRY_PUSH_USER"), Password: os.Getenv("REGISTRY_PUSH_PASSWORD")}
	}
	return Access{Address: address, Username: os.Getenv("REGISTRY_PULL_USER"), Password: os.Getenv("REGISTRY_PULL_PASSWORD")}
}

// DockerConfig creates a private short-lived CLI authentication directory.
func (a Access) DockerConfig(root string) (string, error) {
	d, e := os.MkdirTemp(root, "registry-auth-")
	if e != nil {
		return "", e
	}
	home, _ := os.UserHomeDir()
	v := map[string]any{"cliPluginsExtraDirs": []string{filepath.Join(home, ".docker", "cli-plugins")}, "auths": map[string]any{a.Address: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(a.Username + ":" + a.Password))}}}
	b, e := json.Marshal(v)
	if e == nil {
		e = os.WriteFile(filepath.Join(d, "config.json"), b, 0600)
	}
	if e != nil {
		os.RemoveAll(d)
		return "", e
	}
	return d, nil
}
