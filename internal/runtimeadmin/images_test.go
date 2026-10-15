package runtimeadmin

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/imagebuild"
)

func TestImageRoutesAuthenticatedAndNoImplicitBuild(t *testing.T) {
	called := 0
	s := New(Config{DataDir: t.TempDir(), Image: "agent:current", Secret: "test-secret", ImageRunner: func(context.Context, []string, io.Writer) error { called++; return nil }})
	r := httptest.NewRequest("GET", "/images", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("GET", "/images", nil)
	r.Header.Set("X-SwarmJet-Runtime-Secret", "test-secret")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var view struct {
		Config       imagebuild.Config `json:"config"`
		DefaultImage string            `json:"defaultImage"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 || len(view.Config.Agents) != 0 || view.DefaultImage != "agent:current" {
		t.Fatal(w.Code, err)
	}
	if called != 0 {
		t.Fatal("read triggered Docker or a build")
	}
	r = httptest.NewRequest("PUT", "/images/config", strings.NewReader(`{"agents":["codex"],"baseImage":"node:22-bookworm-slim","extraPackages":["curl; rm -rf /"],"buildArgs":{}}`))
	r.Header.Set("X-SwarmJet-Runtime-Secret", "test-secret")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 || called != 0 {
		t.Fatal("invalid build config accepted", w.Code, called)
	}
}

func TestImageActivationRequiresClosedTerminal(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	s := New(Config{DataDir: t.TempDir(), Image: "agent:current", Secret: "test-secret", ImageRunner: func(ctx context.Context, args []string, out io.Writer) error {
		_, err := io.WriteString(out, digest)
		return err
	}}).(*server)
	b, err := s.imageManager.Import(context.Background(), "agent:new")
	if err != nil {
		t.Fatal(err)
	}
	s.terminal <- struct{}{}
	r := httptest.NewRequest("POST", "/images/builds/"+b.ID+"/activate", nil)
	r.Header.Set("X-SwarmJet-Runtime-Secret", "test-secret")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal("active terminal interrupted", w.Code)
	}
	<-s.terminal
	r = httptest.NewRequest("POST", "/images/builds/"+b.ID+"/activate", nil)
	r.Header.Set("X-SwarmJet-Runtime-Secret", "test-secret")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if current, err := imagebuild.CurrentImage(s.cfg.DataDir, s.cfg.Image); err != nil || current != digest {
		t.Fatal(current, err)
	}
	f := &fakeDocker{name: s.name, identity: s.identity, image: s.cfg.Image}
	s.cfg.DockerCommand = f.run
	if err := s.ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range f.calls {
		if len(call) > 1 && call[0] == "container" && call[1] == "create" {
			found = strings.Contains(strings.Join(call, " "), "/bin/sh "+digest+" -c exec sleep infinity")
		}
	}
	if !found {
		t.Fatal("configuration terminal did not use the applied image")
	}

}
