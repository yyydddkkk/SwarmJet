package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/credentials"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type discoveryFixture struct {
	server         *httptest.Server
	manifest       string
	manifestStatus map[string]int
	files          []string
	lookups        []string
	defaultBranch  string
	branches       []string
	childStatus    int
	prefix         string
	requests       int
}

func newDiscoveryFixture(t *testing.T, prefix string) *discoveryFixture {
	t.Helper()
	f := &discoveryFixture{manifestStatus: map[string]int{}, defaultBranch: "trunk", branches: []string{"main"}, prefix: prefix}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests++
		if r.Method != "GET" {
			t.Error("metadata import attempted a mutation")
		}
		if r.Header.Get("PRIVATE-TOKEN") != "test-private-token" {
			t.Error("credential not supplied")
		}
		suffix := strings.TrimPrefix(r.URL.Path, prefix+"/api/v4/projects/")
		if strings.Contains(suffix, "/repository/files/") {
			file := strings.TrimSuffix(strings.Split(suffix, "/repository/files/")[1], "/raw")
			f.files = append(f.files, file)
			if r.URL.Query().Get("ref") != "trunk" {
				t.Errorf("wrong default meta ref: %s", r.URL.Query().Get("ref"))
			}
			if code := f.manifestStatus[file]; code != 0 {
				w.WriteHeader(code)
				return
			}
			fmt.Fprint(w, f.manifest)
			return
		}
		if strings.Contains(suffix, "/repository/branches") {
			if strings.HasSuffix(suffix, "/branches") {
				rows := []map[string]string{}
				for _, name := range f.branches {
					rows = append(rows, map[string]string{"name": name})
				}
				json.NewEncoder(w).Encode(rows)
				return
			}
			name := strings.Split(suffix, "/branches/")[1]
			for _, branch := range f.branches {
				if branch == name {
					json.NewEncoder(w).Encode(map[string]string{"name": name})
					return
				}
			}
			w.WriteHeader(404)
			return
		}
		f.lookups = append(f.lookups, r.URL.EscapedPath())
		if suffix != "team/meta" && f.childStatus != 0 {
			w.WriteHeader(f.childStatus)
			return
		}
		ids := map[string]int{"team/meta": 10, "team/api": 11, "team/web": 12, "team/core": 13, "team/odd": 14, "team/other": 15}
		id := ids[suffix]
		if id == 0 {
			t.Errorf("unexpected project lookup %s", suffix)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": id, "name": "Project " + suffix, "default_branch": f.defaultBranch, "http_url_to_repo": f.server.URL + prefix + "/" + suffix + ".git"})
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *discoveryFixture) repo(name string) string {
	return f.server.URL + f.prefix + "/team/" + name + ".git"
}
func discoveryServer() *server {
	return &server{cfg: Config{ResolveCredential: func(name string) (string, error) {
		if name != "GITLAB_TOKEN" {
			return "", errors.New("secret content should not escape")
		}
		return "test-private-token", nil
	}}}
}
func TestDiscoverSimpleDefaultsAndMissingTarget(t *testing.T) {
	f := newDiscoveryFixture(t, "")
	f.manifest = "projects:\n  api: " + f.repo("api") + "\n  web: " + f.repo("web") + "\n"
	result, err := discoveryServer().discoverMeta(context.Background(), discoverRequest{MetaURL: f.repo("meta")})
	if err != nil {
		t.Fatal(err)
	}
	if result.MetaRef != "trunk" || result.GitLabURL != f.server.URL || result.Name != "Project team/meta" || len(result.Repositories) != 2 {
		t.Fatalf("%+v", result)
	}
	for _, repository := range result.Repositories {
		if repository.ProjectID <= 0 || repository.TargetBranch != "dev" || repository.CreateFrom != "" {
			t.Fatalf("invented metadata or target fallback: %+v", repository)
		}
	}
	if len(result.Warnings) != 2 || !strings.Contains(result.Warnings[0], "dev") {
		t.Fatalf("missing dev warning: %v", result.Warnings)
	}
	if len(f.files) != 1 || f.files[0] != ".meta.yaml" {
		t.Fatalf("unexpected fallback: %v", f.files)
	}
	if !strings.Contains(f.lookups[0], "team%2Fmeta") {
		t.Fatalf("namespace not encoded: %v", f.lookups)
	}
}

func TestDiscoverUsesSavedConnectionBoundToMetaHost(t *testing.T) {
	f := newDiscoveryFixture(t, "")
	f.manifest = "projects:\n  api: " + f.repo("api") + "\n"
	box := newConnectionBox(t)
	sealed, err := box.Seal(mustJSON(t, credentials.Payload{Token: "test-private-token"}))
	if err != nil {
		t.Fatal(err)
	}
	conns := newFakeConnections()
	conns.items["c1"] = domain.Connection{ID: "c1", GitLabURL: f.server.URL, Version: 1}
	conns.secrets["c1"] = sealed
	a := &server{cfg: Config{Box: box}, conns: conns}
	input := discoverRequest{MetaURL: f.repo("meta"), ConnectionID: "c1"}
	if result, err := a.discoverMeta(context.Background(), input); err != nil || len(result.Repositories) != 1 {
		t.Fatalf("saved connection discovery failed: %+v %v", result, err)
	}
	before := f.requests
	conns.items["c1"] = domain.Connection{ID: "c1", GitLabURL: "https://other.example", Version: 1}
	if _, err := a.discoverMeta(context.Background(), input); err == nil || f.requests != before {
		t.Fatalf("mismatched connection reached Meta-Repo: %v", err)
	}
}

func TestDiscoverWrongManifestShapeExplainsProjectsMapping(t *testing.T) {
	_, err := discoveryManifest([]byte("version: 1\nrepositories:\n  - alias: api\n    url: https://git.example/team/api\n    path: repos/api\n"))
	if err == nil || !strings.Contains(err.Error(), "repositories") || !strings.Contains(err.Error(), "projects") || strings.Contains(err.Error(), "1 到 100") {
		t.Fatalf("misleading manifest diagnostic: %v", err)
	}
}

func TestDiscoverFiveStandardEntriesWithDescriptions(t *testing.T) {
	f := newDiscoveryFixture(t, "")
	f.manifest = "version: 1\nprojects:\n"
	for _, alias := range []string{"api", "web", "core", "odd", "other"} {
		f.manifest += fmt.Sprintf("  %s:\n    description: 保留已有仓库说明\n    repo: %s\n    path: repos/%s\n", alias, f.repo(alias), alias)
	}
	out, err := discoveryServer().discoverMeta(context.Background(), discoverRequest{MetaURL: f.repo("meta")})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Repositories) != 5 {
		t.Fatalf("got %d repositories", len(out.Repositories))
	}
	for _, r := range out.Repositories {
		if r.Path != "repos/"+r.ID || r.ProjectID <= 0 {
			t.Fatalf("metadata lost: %+v", r)
		}
	}
}
func TestDiscoverExtendedDependenciesSubpathAndStableIDs(t *testing.T) {
	f := newDiscoveryFixture(t, "/gitlab")
	f.branches = []string{"dev", "main"}
	f.manifest = fmt.Sprintf("projects:\n  core:\n    repo: %s\n    path: libs/core\n    provides: [storage]\n  services/api:\n    repo: %s\n    depends_on: [storage, libs/core]\n  web:\n    repo: %s\n    path: apps/web\n    depends_on: [services/api]\n", f.repo("core"), f.repo("api"), f.repo("web"))
	input := discoverRequest{MetaURL: f.repo("meta"), GitLabURL: f.server.URL + "/gitlab/api/v4"}
	first, err := discoveryServer().discoverMeta(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := discoveryServer().discoverMeta(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.GitLabURL != f.server.URL+"/gitlab" || len(first.Warnings) != 0 {
		t.Fatalf("%+v", first)
	}
	var apiRepo, webRepo domain.Repository
	for _, repo := range first.Repositories {
		if repo.Path == "services/api" {
			apiRepo = repo
		}
		if repo.Path == "apps/web" {
			webRepo = repo
		}
	}
	if !domain.IDPattern.MatchString(apiRepo.ID) || apiRepo.ID == "services/api" || len(apiRepo.ID) > 80 || apiRepo.URL != f.repo("api") {
		t.Fatalf("unsafe/changed repository metadata: %+v", apiRepo)
	}
	if len(apiRepo.DependsOn) != 1 || apiRepo.DependsOn[0] != "core" || len(webRepo.DependsOn) != 1 || webRepo.DependsOn[0] != apiRepo.ID {
		t.Fatalf("dependencies not resolved: %+v %+v", apiRepo, webRepo)
	}
	for i := range first.Repositories {
		if first.Repositories[i].ID != second.Repositories[i].ID {
			t.Fatal("unstable generated repository ID")
		}
	}
}
func TestDiscoverManifestFallbackOnlyOnNotFound(t *testing.T) {
	for _, code := range []int{404, 401, 403, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f := newDiscoveryFixture(t, "")
			f.manifest = "projects:\n  api: " + f.repo("api") + "\n"
			f.manifestStatus[".meta.yaml"] = code
			_, err := discoveryServer().discoverMeta(context.Background(), discoverRequest{MetaURL: f.repo("meta")})
			if code == 404 {
				if err != nil || len(f.files) != 2 || f.files[1] != ".meta.yml" {
					t.Fatalf("fallback failed %v %v", f.files, err)
				}
			} else if err == nil || len(f.files) != 1 {
				t.Fatalf("permission/server failure masked: %v %v", f.files, err)
			}
		})
	}
}
func TestDiscoverCredentialAndChildLookupFailures(t *testing.T) {
	f := newDiscoveryFixture(t, "")
	f.manifest = "projects:\n  api: " + f.repo("api") + "\n"
	_, err := discoveryServer().discoverMeta(context.Background(), discoverRequest{MetaURL: f.repo("meta"), CredentialRef: "MISSING_TOKEN"})
	if err == nil || !strings.Contains(err.Error(), "MISSING_TOKEN") || strings.Contains(err.Error(), "secret content") || f.requests != 0 {
		t.Fatalf("credential handling: %v requests=%d", err, f.requests)
	}
	f.childStatus = 403
	_, err = discoveryServer().discoverMeta(context.Background(), discoverRequest{MetaURL: f.repo("meta")})
	if err == nil || !strings.Contains(err.Error(), "api") || !strings.Contains(err.Error(), "权限") {
		t.Fatalf("missing child metadata fabricated: %v", err)
	}
}
func TestDiscoverRejectsOffOriginWithoutLeakingToken(t *testing.T) {
	var received atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		t.Error("off-origin endpoint received a request")
	}))
	defer other.Close()
	for _, kind := range []string{"other-port", "protocol", "userinfo", "query", "fragment", "outside-subpath"} {
		t.Run(kind, func(t *testing.T) {
			f := newDiscoveryFixture(t, "/gitlab")
			child := f.repo("api")
			switch kind {
			case "other-port":
				child = other.URL + "/gitlab/team/api.git"
			case "protocol":
				child = strings.Replace(child, "http:", "https:", 1)
			case "userinfo":
				child = strings.Replace(child, "http://", "http://user:password@", 1)
			case "query":
				child += "?secret=x"
			case "fragment":
				child += "#fragment"
			case "outside-subpath":
				child = f.server.URL + "/team/api.git"
			}
			f.manifest = fmt.Sprintf("projects:\n  api: %q\n", child)
			_, err := discoveryServer().discoverMeta(context.Background(), discoverRequest{MetaURL: f.repo("meta"), GitLabURL: f.server.URL + "/gitlab"})
			if err == nil {
				t.Fatal("off-origin or credential-bearing child accepted")
			}
			if len(f.lookups) != 1 {
				t.Fatalf("child queried before URL validation: %v", f.lookups)
			}
		})
	}
	if received.Load() != 0 {
		t.Fatal("token leaked across origins")
	}
}
func TestDiscoverySSHRulesAndUnsafePaths(t *testing.T) {
	if _, err := discoveryBase("", "git@gitlab.example:team/meta.git"); err == nil {
		t.Fatal("guessed HTTP origin from SSH")
	}
	base, _ := url.Parse("https://gitlab.example")
	for _, raw := range []string{"git@gitlab.example:team/meta.git", "ssh://git@gitlab.example/team/meta.git"} {
		got, err := discoveryProjectPath(base, raw)
		if err != nil || got != "team/meta" {
			t.Fatalf("SSH parse %q %v", got, err)
		}
	}
	for _, raw := range []string{"https://user:secret@gitlab.example/team/meta.git", "https://gitlab.example:8443/team/meta.git", "http://gitlab.example/team/meta.git", "git@other.example:team/meta.git", "ssh://git:secret@gitlab.example/team/meta.git", "https://gitlab.example/team/../meta.git"} {
		if _, err := discoveryProjectPath(base, raw); err == nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
	for _, p := range []string{"../escape", ".git/config", ".worktrees/task", "a/../b", "/root", "a\\b"} {
		if discoveryPath(p) == nil {
			t.Fatalf("unsafe path accepted: %s", p)
		}
	}
}
func TestDiscoverNestedMetaAndDependencyErrors(t *testing.T) {
	f := newDiscoveryFixture(t, "")
	for _, manifest := range []string{
		"projects:\n  api:\n    repo: " + f.repo("api") + "\n    meta: true\n",
		"projects:\n  api:\n    repo: " + f.repo("api") + "\n    depends_on: [missing]\n",
		"projects:\n  api:\n    repo: " + f.repo("api") + "\n    depends_on: [web]\n  web:\n    repo: " + f.repo("web") + "\n    depends_on: [api]\n",
	} {
		f.manifest = manifest
		if _, err := discoveryServer().discoverMeta(context.Background(), discoverRequest{MetaURL: f.repo("meta")}); err == nil {
			t.Fatal("invalid/nested dependency configuration accepted")
		}
	}
}
func TestDiscoverIDCollisionAndManifestLimit(t *testing.T) {
	f := newDiscoveryFixture(t, "")
	collision := discoveryID("services/api")
	f.manifest = fmt.Sprintf("projects:\n  services/api: %s\n  %s:\n    repo: %s\n    path: other\n", f.repo("api"), collision, f.repo("other"))
	output, err := discoveryServer().discoverMeta(context.Background(), discoverRequest{MetaURL: f.repo("meta")})
	if err != nil {
		t.Fatal(err)
	}
	if output.Repositories[0].ID == output.Repositories[1].ID {
		t.Fatal("generated ID collided with valid alias")
	}
	var b strings.Builder
	b.WriteString("projects:\n")
	for i := 0; i < 101; i++ {
		fmt.Fprintf(&b, "  repo%d: https://gitlab.example/team/repo%d.git\n", i, i)
	}
	if _, err = discoveryManifest([]byte(b.String())); err == nil {
		t.Fatal("repository limit ignored")
	}
}
func TestDiscoverRequestAndResponseBounded(t *testing.T) {
	handler := discoveryServer()
	request := httptest.NewRequest("POST", "/api/projects/discover", strings.NewReader(`{"metaUrl":"`+strings.Repeat("a", 1<<20)+`"}`))
	response := httptest.NewRecorder()
	handler.discover(response, request)
	if response.Code != 400 {
		t.Fatalf("oversized request status=%d", response.Code)
	}
	f := newDiscoveryFixture(t, "")
	f.manifest = strings.Repeat("x", (1<<20)+1)
	_, err := handler.discoverMeta(context.Background(), discoverRequest{MetaURL: f.repo("meta")})
	if err == nil || !strings.Contains(err.Error(), "1 MiB") {
		t.Fatalf("unbounded response: %v", err)
	}
}

func TestDiscoverDoesNotFollowCredentialBearingRedirect(t *testing.T) {
	var leaked atomic.Int32
	offsite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer offsite.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, offsite.URL+"/capture", http.StatusFound)
	}))
	defer origin.Close()
	_, err := discoveryServer().discoverMeta(context.Background(), discoverRequest{MetaURL: origin.URL + "/team/meta.git"})
	if err == nil || leaked.Load() != 0 {
		t.Fatalf("redirect followed: %v leaked=%d", err, leaked.Load())
	}
}
func TestDiscoverLegacyManifestRootAndExplicitTarget(t *testing.T) {
	f := newDiscoveryFixture(t, "")
	f.branches = []string{"release"}
	f.manifestStatus[".meta.yaml"] = 404
	f.manifestStatus[".meta.yml"] = 404
	f.manifest = fmt.Sprintf(`{"projects":{"root-copy":%q,"api":%q}}`, f.repo("meta"), f.repo("api"))
	output, err := discoveryServer().discoverMeta(context.Background(), discoverRequest{MetaURL: f.repo("meta"), DefaultTargetBranch: "release"})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Repositories) != 1 || output.Repositories[0].ID != "api" || output.Repositories[0].TargetBranch != "release" || len(f.files) != 3 || f.files[2] != ".meta" {
		t.Fatalf("wrong legacy/root import: %+v files=%v", output, f.files)
	}
	f.manifest = fmt.Sprintf("projects:\n  root:\n    repo: %s\n    path: .\n  api: %s\n", f.repo("meta"), f.repo("api"))
	output, err = discoveryServer().discoverMeta(context.Background(), discoverRequest{MetaURL: f.repo("meta"), DefaultTargetBranch: "release"})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Repositories) != 2 || output.Repositories[1].Path != "." || output.Repositories[1].ProjectID != 10 {
		t.Fatalf("explicit root missing: %+v", output.Repositories)
	}
}
func TestDiscoverySSHNamespaceIndependentOfHTTPSubpath(t *testing.T) {
	base, _ := url.Parse("https://gitlab.example/gitlab")
	got, err := discoveryProjectPath(base, "git@gitlab.example:group/repo.git")
	if err != nil || got != "group/repo" {
		t.Fatalf("SSH namespace incorrectly included HTTP relative URL root: %q %v", got, err)
	}
}
