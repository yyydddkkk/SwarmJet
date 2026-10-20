package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRepositoryTokenAndIDAreScopedToOneRepository(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(Config{AppID: 42, PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/installation":
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				t.Errorf("installation lookup is not signed with the App JWT")
			}
			fmt.Fprint(w, `{"id":6,"account":{"login":"owner"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/6/access_tokens":
			var body struct {
				Repositories []string `json:"repositories"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode token scope: %v", err)
			}
			if len(body.Repositories) != 1 || body.Repositories[0] != "repo" {
				t.Errorf("token is not scoped to one repository: %+v", body)
			}
			fmt.Fprintf(w, `{"token":"repo-token","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo":
			if r.Header.Get("Authorization") != "Bearer repo-token" {
				t.Errorf("repository lookup used %q", r.Header.Get("Authorization"))
			}
			fmt.Fprint(w, `{"id":123,"full_name":"owner/repo"}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	app.baseURL = server.URL
	app.http = server.Client()

	token, err := app.RepositoryToken(context.Background(), "owner/repo")
	if err != nil || token.Token != "repo-token" {
		t.Fatalf("RepositoryToken: %+v %v", token, err)
	}
	id, err := app.RepositoryID(context.Background(), "owner/repo")
	if err != nil || id != 123 {
		t.Fatalf("RepositoryID: %d %v", id, err)
	}
}

func TestRepositoryInstallationRejectsOtherAccount(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(Config{AppID: 42, PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":6,"account":{"login":"someone-else"}}`)
	}))
	defer server.Close()
	app.baseURL = server.URL
	app.http = server.Client()

	if _, err := app.RepositoryToken(context.Background(), "owner/repo"); err == nil {
		t.Fatal("accepted a repository owned by another installation account")
	}
}

func TestRepositoryNameValidation(t *testing.T) {
	app := &App{baseURL: "http://127.0.0.1:1", http: &http.Client{Timeout: time.Second}}
	for _, name := range []string{
		"", "owner", "owner/", "/repo", "owner/repo/extra",
		"owner/re po", "owner/repo?x=1", "owner/repo#frag", "owner\\repo/name",
		"owner/..", "../repo", "owner/.", "./repo", "owner/re:po", "owner/re*po",
	} {
		if _, err := app.RepositoryToken(context.Background(), name); err == nil {
			t.Fatalf("accepted repository name %q", name)
		}
	}
}

func TestSplitRepositoryAcceptsGitHubCharacters(t *testing.T) {
	for _, name := range []string{
		"owner/repo",
		"Owner-1/repo.name",
		"owner_1/repo-name_test",
		"a/b",
	} {
		owner, repo, err := splitRepository(name)
		if err != nil || owner+"/"+repo != name {
			t.Fatalf("splitRepository(%q) = %q, %q, %v", name, owner, repo, err)
		}
	}
}
