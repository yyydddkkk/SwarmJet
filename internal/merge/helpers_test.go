package merge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
)

// --- fake Hive store --------------------------------------------------------

type fakeStore struct {
	mu       sync.Mutex
	hive     domain.Hive
	owner    domain.Agent
	ownerErr error
	messages []domain.Message
	inbox    map[domain.Address][]domain.Message
}

func (f *fakeStore) Hive(context.Context, string) (domain.Hive, error) {
	if f.hive.ID == "" {
		return domain.Hive{}, domain.ErrNotFound
	}
	return f.hive, nil
}

func (f *fakeStore) Agent(context.Context, string) (domain.Agent, error) {
	if f.owner.ID == "" {
		return domain.Agent{}, domain.ErrNotFound
	}
	return f.owner, nil
}

func (f *fakeStore) PRAuthor(context.Context, string, int64) (domain.Agent, error) {
	if f.ownerErr != nil {
		return domain.Agent{}, f.ownerErr
	}
	if f.owner.ID == "" {
		return domain.Agent{}, domain.ErrNotFound
	}
	return f.owner, nil
}

func (f *fakeStore) Inbox(_ context.Context, _ string, to domain.Address, _ string, limit int) ([]domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs := f.inbox[to]
	if limit > 0 && len(msgs) > limit {
		msgs = msgs[:limit]
	}
	return append([]domain.Message(nil), msgs...), nil
}

func (f *fakeStore) SendMessage(_ context.Context, m domain.Message) (domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.messages {
		if existing.ID == m.ID {
			if existing.To != m.To || existing.Body != m.Body || existing.Kind != m.Kind {
				return domain.Message{}, domain.ErrConflict
			}
			return existing, nil
		}
	}
	f.messages = append(f.messages, m)
	return m, nil
}

func (f *fakeStore) sent() []domain.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Message(nil), f.messages...)
}

// --- fake attestation store -------------------------------------------------

type fakeAttestations struct {
	mu        sync.Mutex
	latest    domain.Attestation
	latestErr error
	list      []domain.Attestation
	records   []domain.Attestation
	recordErr error
}

func (f *fakeAttestations) RecordAttestation(_ context.Context, a domain.Attestation) (domain.Attestation, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordErr != nil {
		return domain.Attestation{}, false, f.recordErr
	}
	for _, stored := range f.records {
		if stored.HiveID == a.HiveID && stored.PRNumber == a.PRNumber &&
			stored.HeadSHA == a.HeadSHA && stored.AttesterID == a.AttesterID {
			return stored, false, nil
		}
	}
	if a.ID == "" {
		a.ID = fmt.Sprintf("att-%d", len(f.records)+1)
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	f.records = append(f.records, a)
	f.list = append([]domain.Attestation{a}, f.list...)
	if !a.Superseded {
		f.latest = a
		f.latestErr = nil
	}
	return a, true, nil
}

func (f *fakeAttestations) SetAttestationComment(_ context.Context, id, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.records {
		if f.records[i].ID == id {
			f.records[i].CommentURL = url
			if f.latest.ID == id {
				f.latest.CommentURL = url
			}
			return nil
		}
	}
	return domain.ErrNotFound
}

func (f *fakeAttestations) LatestAttestation(context.Context, string, int64) (domain.Attestation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.latestErr != nil {
		return domain.Attestation{}, f.latestErr
	}
	if f.latest.ID == "" {
		return domain.Attestation{}, domain.ErrNotFound
	}
	return f.latest, nil
}

func (f *fakeAttestations) Attestations(context.Context, string, int64) ([]domain.Attestation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Attestation(nil), f.list...), nil
}

func (f *fakeAttestations) SupersedeAttestations(_ context.Context, _ string, _ int64, keepSHA string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.records {
		if f.records[i].HeadSHA != keepSHA {
			f.records[i].Superseded = true
		}
	}
	for i := range f.list {
		if f.list[i].HeadSHA != keepSHA {
			f.list[i].Superseded = true
		}
	}
	if f.latest.HeadSHA != keepSHA {
		f.latest = domain.Attestation{}
	}
	return nil
}

// --- fake methodology -------------------------------------------------------

type fakeMethodology struct {
	manifest domain.MethodologyManifest
	err      error
}

func (f fakeMethodology) ActiveMethodology(context.Context, string) (domain.Methodology, []domain.MethodologyFile, error) {
	if f.err != nil {
		return domain.Methodology{}, nil, f.err
	}
	return domain.Methodology{Manifest: f.manifest}, nil, nil
}

func methodologyWith(policy domain.MergePolicy) fakeMethodology {
	return fakeMethodology{manifest: domain.MethodologyManifest{
		DefaultRole: "worker",
		Roles: map[string]domain.Role{
			"worker":   {Permissions: domain.RolePermissions{Publish: true, Message: true}},
			"verifier": {Permissions: domain.RolePermissions{Message: true, Attest: true}},
			"merger":   {Permissions: domain.RolePermissions{Message: true, Merge: true}},
		},
		Merge: policy,
	}}
}

// --- fake scheduler dependencies --------------------------------------------

type fakeInserter struct {
	mu   sync.Mutex
	jobs []jobs.MergeCheck
}

func (f *fakeInserter) InsertMergeCheck(_ context.Context, job jobs.MergeCheck) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.jobs = append(f.jobs, job)
	return nil
}

func (f *fakeInserter) inserted() []jobs.MergeCheck {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]jobs.MergeCheck(nil), f.jobs...)
}

type fakeChecker struct {
	readiness Readiness
	err       error
}

func (f fakeChecker) Check(context.Context, domain.Hive, int64) (Readiness, error) {
	return f.readiness, f.err
}

type fakeMerger struct {
	outcome   Outcome
	err       error
	merged    bool
	mergeSHA  string
	mergeCall int
}

func (f *fakeMerger) Merge(_ context.Context, _ domain.Hive, _ int64) (Outcome, error) {
	f.merged = true
	f.mergeCall++
	f.mergeSHA = f.outcome.HeadSHA
	return f.outcome, f.err
}

type fakePolicies struct {
	policy domain.MergePolicy
	err    error
}

func (f fakePolicies) Policy(context.Context, string) (domain.MergePolicy, error) {
	return f.policy, f.err
}

// --- httptest GitHub --------------------------------------------------------

type gitHubStub struct {
	mu sync.Mutex

	pull          map[string]any
	required      []any
	rules         []any
	statuses      []any
	checks        []any
	mergeStatus   int
	checkStatus   int
	mergeResponse map[string]any
	queueEntry    any

	mergeRequested bool
	mergeSHA       string
	mergeMethod    string

	commentStatus int
	commentURL    string
	commentBodies []string
}

func testPull() map[string]any {
	return map[string]any{
		"number":          7,
		"state":           "open",
		"draft":           false,
		"merged":          false,
		"html_url":        "https://github.com/acme/widgets/pull/7",
		"mergeable":       true,
		"mergeable_state": "clean",
		"head":            map[string]any{"sha": "head", "ref": "feat"},
		"base": map[string]any{
			"ref":  "main",
			"repo": map[string]any{"full_name": "acme/widgets"},
		},
	}
}

func successCheck(name, conclusion string) map[string]any {
	return map[string]any{
		"id":         1,
		"name":       name,
		"head_sha":   "head",
		"status":     "completed",
		"conclusion": conclusion,
		"app":        map[string]any{"id": 7},
		"output":     map[string]any{"title": "run", "summary": "all good"},
	}
}

func newGitHubSource(t *testing.T, stub *gitHubStub) GitHubSource {
	t.Helper()
	if stub.pull == nil {
		stub.pull = testPull()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		write := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		path := r.URL.Path
		switch {
		case path == "/repositories/1" || path == "/repos/acme/widgets":
			write(map[string]any{
				"id": 1, "full_name": "acme/widgets",
				"allow_merge_commit": true, "allow_squash_merge": true, "allow_rebase_merge": true,
			})
		case strings.HasSuffix(path, "/pulls/7"):
			write(stub.pull)
		case strings.HasSuffix(path, "/protection/required_status_checks"):
			if stub.required == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			write(map[string]any{"checks": stub.required, "contexts": []any{}})
		case strings.Contains(path, "/rules/branches/"):
			write(stub.rules)
		case strings.HasSuffix(path, "/statuses"):
			write(stub.statuses)
		case strings.Contains(path, "/check-runs"):
			if stub.checkStatus != 0 {
				w.WriteHeader(stub.checkStatus)
				write(map[string]any{"message": "checks unavailable"})
				return
			}
			write(map[string]any{"check_runs": stub.checks})
		case path == "/graphql":
			var body struct {
				Query string `json:"query"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if strings.Contains(body.Query, "mutation") {
				write(map[string]any{"data": map[string]any{
					"enqueuePullRequest": map[string]any{"mergeQueueEntry": map[string]any{"id": "entry"}},
				}})
				return
			}
			write(map[string]any{"data": map[string]any{
				"node": map[string]any{"mergeQueueEntry": stub.queueEntry},
			}})
		case r.Method == http.MethodPut && strings.HasSuffix(path, "/pulls/7/merge"):
			if stub.mergeStatus != 0 {
				w.WriteHeader(stub.mergeStatus)
				write(map[string]any{"message": "base branch policy prohibits the merge"})
				return
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			stub.mergeRequested = true
			stub.mergeSHA = body["sha"]
			stub.mergeMethod = body["merge_method"]
			if stub.mergeResponse != nil {
				write(stub.mergeResponse)
				return
			}
			write(map[string]any{"merged": true})
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/issues/7/comments"):
			var body struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			stub.commentBodies = append(stub.commentBodies, body.Body)
			if stub.commentStatus != 0 {
				w.WriteHeader(stub.commentStatus)
				write(map[string]any{"message": "comment failed"})
				return
			}
			url := stub.commentURL
			if url == "" {
				url = "https://github.com/acme/widgets/pull/7#issuecomment-1"
			}
			w.WriteHeader(http.StatusCreated)
			write(map[string]any{"html_url": url})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client := github.NewWithBaseURL(func(context.Context) (string, error) { return "test-token", nil }, server.URL)
	return StaticSource{Client: client, RepositoryID: 1}
}

func testHive() domain.Hive {
	return domain.Hive{ID: "hive-1", Name: "Widgets", Repository: "acme/widgets"}
}

func passAttestation() domain.Attestation {
	return domain.Attestation{
		ID: "att-1", HiveID: "hive-1", PRNumber: 7, HeadSHA: "head",
		AttesterID: "verifier-1", AuthorID: "forager-1", Result: domain.AttestPass, Summary: "works",
	}
}

func newTestService(t *testing.T, stub *gitHubStub, store Store, attestations AttestationStore, methodology MethodologySource, inserter Inserter) *Service {
	t.Helper()
	return New(Options{
		Store:        store,
		Attestations: attestations,
		Methodology:  methodology,
		GitHub:       newGitHubSource(t, stub),
		Inserter:     inserter,
	})
}

func readyStub() *gitHubStub {
	return &gitHubStub{
		pull:          testPull(),
		required:      []any{map[string]any{"context": "test", "app_id": 7}},
		checks:        []any{successCheck("test", "success")},
		mergeResponse: map[string]any{"merged": true},
	}
}

var errNotConfigured = errors.New("not configured")
