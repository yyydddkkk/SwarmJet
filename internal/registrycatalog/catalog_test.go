package registrycatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/registryconfig"
)

func TestListGroupsTagsByDigestAndDeletesManifest(t *testing.T) {
	first := "sha256:" + strings.Repeat("a", 64)
	second := "sha256:" + strings.Repeat("b", 64)
	deletes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "builder" || password != "secret" {
			t.Errorf("request missing registry credentials")
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/v2/swarmjet/agent/tags/list" && r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/swarmjet/agent/tags/list?n=100&last=b>; rel="next"`)
			w.Write([]byte(`{"name":"swarmjet/agent","tags":["a","b"]}`))
		case r.Method == "GET" && r.URL.Query().Get("last") == "b":
			w.Write([]byte(`{"name":"swarmjet/agent","tags":["c"]}`))
		case r.Method == "HEAD" && strings.HasPrefix(r.URL.Path, "/v2/swarmjet/agent/manifests/"):
			if !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.index.v1+json") {
				t.Error("manifest Accept header missing OCI index")
			}
			if strings.HasSuffix(r.URL.Path, "/c") {
				w.Header().Set("Docker-Content-Digest", second)
			} else {
				w.Header().Set("Docker-Content-Digest", first)
			}
		case r.Method == "DELETE" && r.URL.Path == "/v2/swarmjet/agent/manifests/"+first:
			deletes++
			w.WriteHeader(202)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := Client{HTTP: srv.Client(), BaseURL: srv.URL, Repository: "swarmjet/agent", Access: registryconfig.Access{Address: "registry.test", Username: "builder", Password: "secret"}}
	images, err := c.List(context.Background())
	if err != nil || len(images) != 2 {
		t.Fatalf("images=%+v err=%v", images, err)
	}
	if images[0].Digest != second || !reflect.DeepEqual(images[1].Tags, []string{"a", "b"}) || images[1].Reference != "registry.test/swarmjet/agent@"+first {
		t.Fatalf("unexpected grouped images: %+v", images)
	}
	if err = c.Delete(context.Background(), first); err != nil || deletes != 1 {
		t.Fatalf("delete count=%d err=%v", deletes, err)
	}
}

func TestMissingRepositoryHasNoImages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := Client{HTTP: srv.Client(), BaseURL: srv.URL, Repository: "swarmjet/agent"}
	images, err := c.List(context.Background())
	if err != nil || len(images) != 0 {
		t.Fatalf("images=%v err=%v", images, err)
	}
}

func TestRegistryModesUseHTTPS(t *testing.T) {
	builtin := New(registryconfig.Config{Mode: "builtin", Address: "10.0.0.20:5443"}, registryconfig.Access{})
	if builtin.BaseURL != "https://10.0.0.20:5443" {
		t.Fatal(builtin.BaseURL)
	}
	custom := New(registryconfig.Config{Mode: "custom", Address: "registry.example:5443"}, registryconfig.Access{})
	if custom.BaseURL != "https://registry.example:5443" {
		t.Fatal(custom.BaseURL)
	}
}
