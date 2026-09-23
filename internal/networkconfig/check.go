package networkconfig

import (
	"context"
	"fmt"
	"golang.org/x/net/http/httpproxy"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func ValidateTarget(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\x00") {
		return fmt.Errorf("测试地址必须为无凭据、查询参数的 HTTP(S) 地址")
	}
	return nil
}
func Check(ctx context.Context, target string, env map[string]string) (int, error) {
	if e := ValidateTarget(target); e != nil {
		return 0, e
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cfg := httpproxy.Config{HTTPProxy: env["HTTP_PROXY"], HTTPSProxy: env["HTTPS_PROXY"], NoProxy: env["NO_PROXY"]}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(r *http.Request) (*url.URL, error) { return cfg.ProxyFunc()(r.URL) }
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, e := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if e != nil {
		return 0, e
	}
	resp, e := client.Do(req)
	if e != nil {
		return 0, fmt.Errorf("连接失败，请检查目标地址、代理与证书")
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}
