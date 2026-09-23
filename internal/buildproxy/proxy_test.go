package buildproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestDirectProxyAndNoProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "target") }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "upstream") }))
	defer upstream.Close()
	p := &Proxy{}
	srv := httptest.NewServer(p)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
	resp, e := client.Get(target.URL)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Fatal(resp.StatusCode)
	}
	p.Use(map[string]string{})
	resp, e = client.Get(target.URL)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "target" {
		t.Fatal(string(b))
	}
	p.Use(map[string]string{"HTTP_PROXY": upstream.URL})
	resp, e = client.Get("http://external.example/resource")
	if e != nil {
		t.Fatal(e)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "upstream" {
		t.Fatal(string(b))
	}
	p.Use(map[string]string{"HTTP_PROXY": upstream.URL, "NO_PROXY": "127.0.0.1"})
	resp, e = client.Get(target.URL)
	if e != nil {
		t.Fatal(e)
	}
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "target" {
		t.Fatal(string(b))
	}
}

func TestConnectTunnelKeepsTLSValidation(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "secure-target") }))
	defer target.Close()
	p := &Proxy{}
	p.Use(map[string]string{})
	server := httptest.NewServer(p)
	defer server.Close()
	proxyURL, _ := url.Parse(server.URL)
	transport := target.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	resp, e := client.Get(target.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "secure-target" {
		t.Fatal(string(b))
	}
}
