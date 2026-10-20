package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology"
	"github.com/yyydddkkk/SwarmJet/internal/methodology/catalog"
)

func TestMethodologyTemplateAPISessionAndCSRF(t *testing.T) {
	_, handler, cookie := newHiveAPITest(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/methodology-templates"},
		{http.MethodPost, "/api/hives/missing/methodology/install"},
	} {
		if w := hiveDo(t, handler, nil, tc.method, tc.path, map[string]any{"name": "pstack"}); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s %s: %d", tc.method, tc.path, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "https://console.test/api/hives/missing/methodology/install", strings.NewReader(`{"name":"pstack"}`))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "https://attacker.test")
	r.Header.Set("X-Requested-With", "swarmjet")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin install status = %d", w.Code)
	}
}

func TestMethodologyTemplateAPIInstallsVersionAndNotifies(t *testing.T) {
	s, handler, cookie := newHiveAPITest(t)
	hive := createHiveForTest(t, handler, cookie, createHiveRequest{Name: "Templates", Repository: "acme/templates"})
	templates := hiveDecode[[]domain.MethodologyTemplate](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/methodology-templates", nil), http.StatusOK)
	wantTemplates, err := catalog.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) == 0 || !reflect.DeepEqual(templates, wantTemplates) {
		t.Fatalf("templates = %+v, want %+v", templates, wantTemplates)
	}
	template, manifest, files, err := catalog.Get(templates[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/hives/" + hive.ID + "/methodology/install"
	for version := 1; version <= 2; version++ {
		installed := hiveDecode[methodologyResponse](t, hiveDo(t, handler, cookie, http.MethodPost, path,
			map[string]any{"name": template.Name, "note": "install from console"}), http.StatusCreated)
		if installed.Version != version || !installed.Active || installed.Builtin || installed.Digest == "" || installed.CreatedBy != domain.LocalAddress("web-admin") || installed.Note != "install from console" {
			t.Fatalf("installed = %+v", installed)
		}
		if installed.Source == nil || installed.Source.URL != "builtin:"+template.Name || installed.Source.Ref != template.Version {
			t.Fatalf("source = %+v", installed.Source)
		}
		if !reflect.DeepEqual(installed.Manifest, manifest) || !reflect.DeepEqual(installed.Files, files) {
			t.Fatal("installed content differs from catalog")
		}
	}
	active := hiveDecode[methodologyResponse](t, hiveDo(t, handler, cookie, http.MethodGet,
		"/api/hives/"+hive.ID+"/methodology", nil), http.StatusOK)
	if active.Version != 2 || active.Source == nil || active.Source.URL != "builtin:"+template.Name {
		t.Fatalf("active = %+v", active)
	}
	changes, err := s.MethodologyChanges(context.Background(), hive.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || changes[1].Action != methodology.ActionPublish || changes[1].Note != "install from console" {
		t.Fatalf("changes = %+v", changes)
	}
	inbox, err := s.Inbox(context.Background(), hive.ID, domain.AddressHive, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 2 || inbox[0].From != domain.AddressSystem || !strings.Contains(inbox[0].Body, "methodology publish") {
		t.Fatalf("notifications = %+v", inbox)
	}
}

func TestMethodologyTemplateAPIValidatesInstall(t *testing.T) {
	s, handler, cookie := newHiveAPITest(t)
	hive := createHiveForTest(t, handler, cookie, createHiveRequest{Name: "Invalid", Repository: "acme/invalid"})
	path := "/api/hives/" + hive.ID + "/methodology/install"
	for _, tc := range []struct {
		name   string
		body   any
		status int
	}{
		{"missing name", map[string]any{}, http.StatusBadRequest},
		{"blank name", map[string]any{"name": "  "}, http.StatusBadRequest},
		{"unknown template", map[string]any{"name": "unknown"}, http.StatusNotFound},
		{"path traversal", map[string]any{"name": "../pstack"}, http.StatusNotFound},
		{"long note", map[string]any{"name": "pstack", "note": strings.Repeat("界", maxMethodologyNoteRunes+1)}, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := hiveDo(t, handler, cookie, http.MethodPost, path, tc.body); w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet, path, nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET install status = %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/methodology-templates", map[string]any{}); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST catalog status = %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/hives/missing/methodology/install", map[string]any{"name": "pstack"}); w.Code != http.StatusNotFound {
		t.Fatalf("missing hive status = %d", w.Code)
	}
	versions, err := s.MethodologyVersions(context.Background(), hive.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 0 {
		t.Fatalf("invalid requests published versions: %+v", versions)
	}
}
