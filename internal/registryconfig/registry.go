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
	"swarmjet/internal/secretbox"
	"swarmjet/internal/store"
)

type Config struct {
	Address         string `json:"address"`
	Repository      string `json:"repository"`
	PushUser        string `json:"pushUser"`
	PullUser        string `json:"pullUser"`
	HasPushPassword bool   `json:"hasPushPassword"`
	HasPullPassword bool   `json:"hasPullPassword"`
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
		return v.Config, Access{}, e
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
