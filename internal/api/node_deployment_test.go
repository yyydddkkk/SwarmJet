package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestNodeBundleRequestRejectsUnsafeDeploymentValues(t *testing.T) {
	address := "10.0.0.20:5443"
	valid := nodeBundleRequest{
		Image:   address + "/swarmjet/platform@sha256:" + strings.Repeat("a", 64),
		DataDir: "/srv/swarmjet/node-data", AdvertiseURL: "http://10.0.0.21:8092",
		ControlPlaneURL: "http://10.0.0.20:8180", Capacity: 2, Port: 8092, ListenHost: "0.0.0.0",
	}
	if err := valid.validate(address); err != nil {
		t.Fatal(err)
	}
	cases := []nodeBundleRequest{
		func() nodeBundleRequest {
			v := valid
			v.Image = "example.com/swarmjet/platform@sha256:" + strings.Repeat("a", 64)
			return v
		}(),
		func() nodeBundleRequest { v := valid; v.DataDir = "/srv/../tmp"; return v }(),
		func() nodeBundleRequest { v := valid; v.AdvertiseURL = "http://user:pass@10.0.0.21:8092"; return v }(),
		func() nodeBundleRequest {
			v := valid
			v.ControlPlaneURL = "http://10.0.0.20:8180/api?token=x"
			return v
		}(),
		func() nodeBundleRequest { v := valid; v.Port = 0; return v }(),
	}
	for _, candidate := range cases {
		if err := candidate.validate(address); err == nil {
			t.Fatalf("accepted unsafe bundle request: %#v", candidate)
		}
	}
}

func TestNodeBundleKeepsCredentialsInPrivateFile(t *testing.T) {
	secret := "test-only-secret"
	config, err := json.Marshal(map[string]string{"nodeSecret": secret})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := makeNodeBundle(map[string][]byte{
		"install_node.py": []byte("print('hello')\n"), "compose.node.yaml": []byte("services: {}\n"),
		"upgrade_host.py": []byte("print('upgrade')\n"), "Dockerfile.updater": []byte("FROM scratch\n"),
		"registry-ca.crt": []byte("certificate"), "node.json": config, "README.txt": []byte("instructions"),
	})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), secret) != (h.Name == "node.json") {
			t.Fatalf("secret in wrong bundle file: %s", h.Name)
		}
		if h.Name == "node.json" && h.Mode != 0600 {
			t.Fatalf("node.json mode = %o", h.Mode)
		}
		if h.ModTime.Before(time.Now().Add(-time.Minute)) || h.ModTime.After(time.Now().Add(time.Minute)) {
			t.Fatalf("unexpected archive time for %s: %s", h.Name, h.ModTime)
		}
		seen[h.Name] = true
	}
	if len(seen) != 7 {
		t.Fatalf("bundle files = %v", seen)
	}
}
