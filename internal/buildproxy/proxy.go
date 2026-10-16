// Package buildproxy forwards HTTP(S) traffic through a selected proxy profile.
// The Mihomo gateway uses it; its listener is private to the deployment network.
package buildproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"golang.org/x/net/http/httpproxy"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type Proxy struct {
	// ForceProxy is used by the authenticated Mihomo gateway, including loopback targets.
	ForceProxy bool
	mu         sync.RWMutex
	profile    *httpproxy.Config
}

func (p *Proxy) Use(env map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.profile = &httpproxy.Config{HTTPProxy: env["HTTP_PROXY"], HTTPSProxy: env["HTTPS_PROXY"], NoProxy: env["NO_PROXY"]}
}
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	cfg := p.profile
	p.mu.RUnlock()
	if cfg == nil {
		http.Error(w, "no active build network", 503)
		return
	}
	destination := r.URL
	if r.Method == http.MethodConnect {
		destination = &url.URL{Scheme: "https", Host: r.Host}
	}
	if destination.Host == "" || (destination.Scheme != "http" && destination.Scheme != "https") {
		http.Error(w, "invalid proxy target", 400)
		return
	}
	upstream, e := cfg.ProxyFunc()(destination)
	if p.ForceProxy {
		raw := cfg.HTTPProxy
		if destination.Scheme == "https" {
			raw = cfg.HTTPSProxy
		}
		upstream, e = url.Parse(raw)
		if raw == "" || e == nil && upstream.Host == "" {
			e = fmt.Errorf("upstream required")
		}
	}
	if e != nil {
		http.Error(w, "invalid upstream proxy", 502)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r, upstream)
		return
	}
	transport := &http.Transport{Proxy: http.ProxyURL(upstream), DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 60 * time.Second}
	defer transport.CloseIdleConnections()
	req := r.Clone(r.Context())
	req.RequestURI = ""
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	resp, e := transport.RoundTrip(req)
	if e != nil {
		http.Error(w, "build network request failed", 502)
		return
	}
	defer resp.Body.Close()
	for k, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request, upstream *url.URL) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	target := r.Host
	if _, _, e := net.SplitHostPort(target); e != nil {
		http.Error(w, "CONNECT requires host:port", 400)
		return
	}
	dial := &net.Dialer{Timeout: 15 * time.Second}
	address := target
	if upstream != nil {
		address = upstream.Host
		if upstream.Port() == "" {
			port := "80"
			if upstream.Scheme == "https" {
				port = "443"
			}
			address = net.JoinHostPort(upstream.Hostname(), port)
		}
	}
	conn, e := dial.DialContext(ctx, "tcp", address)
	if e != nil {
		http.Error(w, "build network connect failed", 502)
		return
	}
	if upstream != nil && upstream.Scheme == "https" {
		secure := tls.Client(conn, &tls.Config{ServerName: upstream.Hostname(), MinVersion: tls.VersionTLS12})
		if e = secure.HandshakeContext(ctx); e != nil {
			conn.Close()
			http.Error(w, "upstream TLS failed", 502)
			return
		}
		conn = secure
	}
	var reader io.Reader = conn
	if upstream != nil {
		_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
		req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: make(http.Header)}
		if upstream.User != nil {
			pass, _ := upstream.User.Password()
			req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(upstream.User.Username()+":"+pass)))
		}
		if e = req.Write(conn); e != nil {
			conn.Close()
			http.Error(w, "upstream connect failed", 502)
			return
		}
		br := bufio.NewReader(conn)
		resp, e := http.ReadResponse(br, req)
		if e != nil || resp.StatusCode != 200 {
			conn.Close()
			http.Error(w, "upstream refused tunnel", 502)
			return
		}
		reader = br
		_ = conn.SetDeadline(time.Time{})
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		conn.Close()
		http.Error(w, "tunnel unavailable", 500)
		return
	}
	client, buffer, e := hijacker.Hijack()
	if e != nil {
		conn.Close()
		return
	}
	defer conn.Close()
	defer client.Close()
	_, _ = fmt.Fprint(buffer, "HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = buffer.Flush()
	done := make(chan struct{})
	go func() { _, _ = io.Copy(conn, buffer); conn.Close(); close(done) }()
	_, _ = io.Copy(client, reader)
	client.Close()
	<-done
}
