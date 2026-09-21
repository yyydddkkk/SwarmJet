package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarmjet/internal/credentials"
	"swarmjet/internal/domain"
	"swarmjet/internal/secretbox"
	"swarmjet/internal/store"
)

type fakeConnections struct {
	items   map[string]domain.Connection
	secrets map[string]string
}

func newFakeConnections() *fakeConnections {
	return &fakeConnections{items: map[string]domain.Connection{}, secrets: map[string]string{}}
}
func (f *fakeConnections) SaveConnection(_ context.Context, c domain.Connection, secret string) (domain.Connection, error) {
	if existing, ok := f.items[c.ID]; ok && existing.Version != c.Version {
		return c, store.ErrConflict
	}
	if _, ok := f.items[c.ID]; !ok && c.Version != 0 {
		return c, store.ErrConflict
	}
	c.Version++
	f.items[c.ID] = c
	f.secrets[c.ID] = secret
	return c, nil
}
func (f *fakeConnections) Connection(_ context.Context, id string) (domain.Connection, error) {
	c, ok := f.items[id]
	if !ok {
		return domain.Connection{}, store.ErrNotFound
	}
	return c, nil
}
func (f *fakeConnections) Connections(context.Context) ([]domain.Connection, error) {
	out := []domain.Connection{}
	for _, c := range f.items {
		out = append(out, c)
	}
	return out, nil
}
func (f *fakeConnections) ConnectionSecrets(_ context.Context, id string) (string, error) {
	secret, ok := f.secrets[id]
	if !ok {
		return "", store.ErrNotFound
	}
	return secret, nil
}
func (f *fakeConnections) DeleteConnection(_ context.Context, id string) error {
	if _, ok := f.items[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.items, id)
	delete(f.secrets, id)
	return nil
}

func newConnectionBox(t *testing.T) *secretbox.Box {
	t.Helper()
	box, err := secretbox.New("00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func gitlabUserServer(t *testing.T, status int, username string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/user" {
			w.WriteHeader(404)
			return
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "username": username, "name": "Test User"})
	}))
	t.Cleanup(server.Close)
	return server
}

func callHandler(t *testing.T, method, path string, body any, handler func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(mustJSON(t, body)))
	}
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(method, path, reader))
	return rec
}

func TestConnectionVerifySucceedsWithoutLeakingToken(t *testing.T) {
	remote := gitlabUserServer(t, 200, "operator")
	apiServer := &server{cfg: Config{Box: newConnectionBox(t)}}
	rec := callHandler(t, "POST", "/api/connections/verify", map[string]any{"gitlabUrl": remote.URL, "token": "glpat-signature-value"}, apiServer.verifyConnection)
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "glpat-signature-value") {
		t.Fatal("token leaked in verification response")
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["ok"] != true || body["username"] != "operator" {
		t.Fatalf("unexpected body %s", rec.Body)
	}
	if body["fingerprint"] == "" || body["fingerprint"] == "glpat-signature-value" {
		t.Fatalf("fingerprint invalid: %v", body["fingerprint"])
	}
}

func TestConnectionVerifyRejectsInvalidToken(t *testing.T) {
	remote := gitlabUserServer(t, 401, "")
	apiServer := &server{cfg: Config{Box: newConnectionBox(t)}}
	rec := callHandler(t, "POST", "/api/connections/verify", map[string]any{"gitlabUrl": remote.URL, "token": "glpat-rejected-value"}, apiServer.verifyConnection)
	if rec.Code != 401 || strings.Contains(rec.Body.String(), "glpat-rejected-value") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "Token") {
		t.Fatalf("missing actionable message: %s", rec.Body)
	}
}

func TestConnectionCreateEncryptsTokenAndUpdates(t *testing.T) {
	remote := gitlabUserServer(t, 200, "operator")
	box := newConnectionBox(t)
	conns := newFakeConnections()
	apiServer := &server{cfg: Config{Box: box}, conns: conns}
	rec := callHandler(t, "POST", "/api/connections", map[string]any{"name": "团队 GitLab", "gitlabUrl": remote.URL, "token": "glpat-plaintext-value"}, apiServer.connections)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "glpat-plaintext-value") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	var saved domain.Connection
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil || saved.Version != 1 || saved.Username != "operator" {
		t.Fatalf("unexpected saved metadata: %+v %v", saved, err)
	}
	sealed := conns.secrets[saved.ID]
	if sealed == "" || sealed == "glpat-plaintext-value" || strings.Contains(sealed, "glpat") {
		t.Fatalf("secret not encrypted at rest: %q", sealed)
	}
	plain, err := box.Open(sealed)
	var payload credentials.Payload
	if err != nil || json.Unmarshal([]byte(plain), &payload) != nil || payload.Token != "glpat-plaintext-value" {
		t.Fatalf("stored credential invalid: %v %s", err, plain)
	}
	rotate := func(w http.ResponseWriter, r *http.Request) { apiServer.connection(w, r, saved.ID, nil) }
	rec = callHandler(t, "PUT", "/api/connections/"+saved.ID, map[string]any{"name": "团队 GitLab", "gitlabUrl": remote.URL, "version": saved.Version, "token": "glpat-rotated-value"}, rotate)
	if rec.Code != 200 {
		t.Fatalf("rotate status %d body %s", rec.Code, rec.Body)
	}
	rotated := conns.secrets[saved.ID]
	plain, _ = box.Open(rotated)
	payload = credentials.Payload{}
	_ = json.Unmarshal([]byte(plain), &payload)
	if payload.Token != "glpat-rotated-value" || rotated == sealed {
		t.Fatalf("rotation failed: %q", payload.Token)
	}
}

func TestConnectionRepositoriesSendsSavedTokenAndPaginates(t *testing.T) {
	box := newConnectionBox(t)
	sealed, err := box.Seal(mustJSON(t, credentials.Payload{Token: "glpat-saved-value"}))
	if err != nil {
		t.Fatal(err)
	}
	seen := ""
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/projects" {
			w.WriteHeader(404)
			return
		}
		seen = r.Header.Get("PRIVATE-TOKEN")
		w.Header().Set("X-Next-Page", "2")
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 11, "name": "API", "path_with_namespace": "team/api", "default_branch": "dev", "http_url_to_repo": "http://" + r.Host + "/team/api.git"}})
	}))
	defer remote.Close()
	conns := newFakeConnections()
	conns.items["c1"] = domain.Connection{ID: "c1", Name: "n", GitLabURL: remote.URL, Version: 1}
	conns.secrets["c1"] = sealed
	apiServer := &server{cfg: Config{Box: box}, conns: conns}
	rec := callHandler(t, "GET", "/api/connections/c1/repositories?search=api&page=1&perPage=50", nil, func(w http.ResponseWriter, r *http.Request) {
		apiServer.connection(w, r, "c1", []string{"repositories"})
	})
	if rec.Code != 200 || seen != "glpat-saved-value" {
		t.Fatalf("saved token not used: status=%d token=%q body=%s", rec.Code, seen, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "hasNext") || !strings.Contains(rec.Body.String(), "team/api") {
		t.Fatalf("unexpected repository page: %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "glpat-saved-value") {
		t.Fatal("token leaked in repository listing")
	}
}

func TestCredentialResolvesConnectionReference(t *testing.T) {
	box := newConnectionBox(t)
	sealed, _ := box.Seal(mustJSON(t, credentials.Payload{Token: "primary", MergeToken: "merge"}))
	conns := newFakeConnections()
	conns.items["c1"] = domain.Connection{ID: "c1", Version: 1}
	conns.secrets["c1"] = sealed
	apiServer := &server{cfg: Config{Box: box}, conns: conns}
	if token, err := apiServer.credential(domain.ConnectionCredentialRef("c1")); err != nil || token != "primary" {
		t.Fatalf("primary token: %q %v", token, err)
	}
	if token, err := apiServer.credential(domain.ConnectionMergeCredentialRef("c1")); err != nil || token != "merge" {
		t.Fatalf("merge token: %q %v", token, err)
	}
}

func TestCredentialUnavailableWithoutMasterKey(t *testing.T) {
	conns := newFakeConnections()
	conns.secrets["c1"] = "ciphertext"
	apiServer := &server{cfg: Config{}, conns: conns}
	if _, err := apiServer.credential(domain.ConnectionCredentialRef("c1")); err == nil || !strings.Contains(err.Error(), "MASTER_KEY") {
		t.Fatalf("expected actionable disabled message, got %v", err)
	}
}
