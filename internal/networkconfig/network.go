// Package networkconfig resolves versioned profiles into ephemeral process
// environments. Only references belong in task and workflow history.
package networkconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"swarmjet/internal/domain"
	"swarmjet/internal/secretbox"
	"swarmjet/internal/store"
	"time"
)

var Keys = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy"}

func Direct() map[string]string {
	out := map[string]string{}
	for _, k := range Keys {
		out[k] = ""
	}
	return out
}
func Resolve(ctx context.Context, s *store.Store, box *secretbox.Box, ref domain.NetworkVersion) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out := Direct()
	if ref.ID == "" {
		return out, nil
	}
	latest, _, e := s.NetworkProfile(ctx, domain.NetworkVersion{ID: ref.ID})
	if e != nil {
		return nil, e
	}
	if latest.Revoked {
		return nil, fmt.Errorf("网络配置已撤销")
	}
	p, sealed, e := s.NetworkProfile(ctx, ref)
	if e != nil {
		return nil, e
	}
	password := ""
	if sealed != "" {
		if box == nil {
			return nil, fmt.Errorf("网络凭据加密未配置")
		}
		password, e = box.Open(sealed)
		if e != nil {
			return nil, e
		}
	}
	for key, raw := range map[string]string{"HTTP_PROXY": p.HTTPProxy, "HTTPS_PROXY": p.HTTPSProxy} {
		if raw != "" {
			u, e := url.Parse(raw)
			if e != nil {
				return nil, fmt.Errorf("代理配置无效")
			}
			if p.Username != "" {
				u.User = url.UserPassword(p.Username, password)
			}
			raw = u.String()
		}
		out[key] = raw
	}
	out["NO_PROXY"] = p.NoProxy
	out["http_proxy"] = out["HTTP_PROXY"]
	out["https_proxy"] = out["HTTPS_PROXY"]
	out["no_proxy"] = out["NO_PROXY"]
	return out, nil
}

func NodeSecret(ctx context.Context, s *store.Store, box *secretbox.Box, id, fallback string) (string, error) {
	sealed, revoked, e := s.NodeCredential(ctx, id)
	if e == store.ErrNotFound {
		return fallback, nil
	}
	if e != nil {
		return "", e
	}
	if revoked {
		return "", fmt.Errorf("节点凭据已撤销")
	}
	if box == nil {
		return "", fmt.Errorf("节点凭据加密未配置")
	}
	return box.Open(sealed)
}

// Redact removes proxy URLs and their credentials from durable diagnostics.
func Redact(text string, env map[string]string) string {
	values := []string{}
	for k, v := range env {
		upper := strings.ToUpper(k)
		if v == "" || upper == "NO_PROXY" {
			continue
		}
		if strings.Contains(upper, "PROXY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "SECRET") || strings.HasSuffix(upper, "_KEY") {
			values = append(values, v)
			if u, e := url.Parse(v); e == nil && u.User != nil {
				p, _ := u.User.Password()
				if p != "" {
					values = append(values, p, url.QueryEscape(p), url.PathEscape(p))
				}
			}
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		text = strings.ReplaceAll(text, v, "[REDACTED]")
		encoded, _ := json.Marshal(v)
		if len(encoded) > 2 {
			text = strings.ReplaceAll(text, string(encoded[1:len(encoded)-1]), "[REDACTED]")
		}
	}
	return text
}
