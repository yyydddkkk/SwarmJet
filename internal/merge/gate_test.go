package merge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
)

// --- fake verdict storage ---------------------------------------------------

type fakeVerdicts struct {
	latest domain.Verdict
	err    error
	list   []domain.Verdict
}

func (f fakeVerdicts) LatestVerdict(context.Context, string, int64) (domain.Verdict, error) {
	return f.latest, f.err
}

func (f fakeVerdicts) Verdicts(context.Context, string, int64) ([]domain.Verdict, error) {
	return f.list, nil
}

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

func (f *fakeStore) Agent(_ context.Context, id string) (domain.Agent, error) {
	if f.owner.ID == id {
		return f.owner, nil
	}
	return domain.Agent{}, domain.ErrNotFound
}

func (f *fakeStore) ForagerByPR(context.Context, string, int64) (domain.Agent, error) {
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

// --- httptest GitHub --------------------------------------------------------

type gitHubStub struct {
	mu sync.Mutex

	pull          map[string]any
	required      []any
	rules         []any
	statuses      []any
	checks        []any
	mergeStatus   int
	mergeResponse map[string]any
	queueEntry    any

	mergeRequested bool
	mergeSHA       string
	mergeMethod    string
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		write := func(v any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		path := r.URL.Path
		switch {
		case path == "/repos/acme/widgets" || path == "/repositories/1":
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
		case strings.HasSuffix(path, "/check-runs"):
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
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client := github.NewWithBaseURL(func(context.Context) (string, error) { return "test-token", nil }, server.URL)
	return StaticSource{Client: client}
}

func testHive() domain.Hive {
	return domain.Hive{
		ID:         "hive-1",
		Repository: "acme/widgets",
		Config:     domain.HiveConfig{Verify: domain.VerifyConfig{AutoMerge: false}},
	}
}

func passVerdict() domain.Verdict {
	return domain.Verdict{
		HiveID: "hive-1", PRNumber: 7, HeadSHA: "head", OwnerID: "forager-1",
		VerifierID: "verifier-1", Result: domain.VerdictPass, Summary: "works",
	}
}

func newTestGate(t *testing.T, stub *gitHubStub, verdicts VerdictReader, store *fakeStore) *Gate {
	t.Helper()
	if stub.pull == nil {
		stub.pull = testPull()
	}
	return NewGate(verdicts, store, newGitHubSource(t, stub), Options{})
}

func TestGateReady(t *testing.T) {
	stub := &gitHubStub{
		required: []any{map[string]any{"context": "test", "app_id": 7}},
		checks:   []any{successCheck("test", "success")},
	}
	gate := newTestGate(t, stub, fakeVerdicts{latest: passVerdict()}, &fakeStore{hive: testHive()})
	readiness, err := gate.Check(context.Background(), testHive(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready || len(readiness.Reasons) != 0 {
		t.Fatalf("readiness = %+v", readiness)
	}
	if readiness.HeadSHA != "head" || !readiness.VerdictFound || readiness.Verdict.Result != domain.VerdictPass {
		t.Fatalf("readiness = %+v", readiness)
	}
	if len(readiness.Checks) != 1 || readiness.Checks[0].Name != "test" {
		t.Fatalf("checks = %+v", readiness.Checks)
	}
}

func TestGateRuleReasons(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*gitHubStub, *domain.Verdict)
		verdicts VerdictReader
		want     string
	}{
		{
			name:     "closed",
			mutate:   func(s *gitHubStub, _ *domain.Verdict) { s.pull["state"] = "closed" },
			verdicts: fakeVerdicts{latest: passVerdict()},
			want:     "not open",
		},
		{
			name:     "draft",
			mutate:   func(s *gitHubStub, _ *domain.Verdict) { s.pull["draft"] = true },
			verdicts: fakeVerdicts{latest: passVerdict()},
			want:     "draft",
		},
		{
			name: "wrong base repository",
			mutate: func(s *gitHubStub, _ *domain.Verdict) {
				s.pull["base"] = map[string]any{"ref": "main", "repo": map[string]any{"full_name": "acme/other"}}
			},
			verdicts: fakeVerdicts{latest: passVerdict()},
			want:     "not the Hive repository",
		},
		{
			name:     "missing verdict",
			verdicts: fakeVerdicts{err: domain.ErrNotFound},
			want:     "no verdict",
		},
		{
			name: "stale verdict",
			verdicts: fakeVerdicts{latest: func() domain.Verdict {
				v := passVerdict()
				v.HeadSHA = "old"
				return v
			}()},
			want: "latest verdict is for",
		},
		{
			name:     "fail verdict",
			verdicts: fakeVerdicts{latest: func() domain.Verdict { v := passVerdict(); v.Result = domain.VerdictFail; return v }()},
			want:     "verdict is fail",
		},
		{
			name:     "blocked verdict",
			verdicts: fakeVerdicts{latest: func() domain.Verdict { v := passVerdict(); v.Result = domain.VerdictBlocked; return v }()},
			want:     "verdict is blocked",
		},
		{
			name: "required check failed",
			mutate: func(s *gitHubStub, _ *domain.Verdict) {
				s.required = []any{map[string]any{"context": "test", "app_id": 7}}
				s.checks = []any{successCheck("test", "failure")}
			},
			verdicts: fakeVerdicts{latest: passVerdict()},
			want:     "required check failed",
		},
		{
			name: "conflict",
			mutate: func(s *gitHubStub, _ *domain.Verdict) {
				s.pull["mergeable"] = false
				s.pull["mergeable_state"] = "dirty"
			},
			verdicts: fakeVerdicts{latest: passVerdict()},
			want:     "conflict",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &gitHubStub{
				required: []any{map[string]any{"context": "test", "app_id": 7}},
				checks:   []any{successCheck("test", "success")},
			}
			stub.pull = testPull()
			if tc.mutate != nil {
				tc.mutate(stub, nil)
			}
			store := &fakeStore{hive: testHive(), owner: domain.Agent{ID: "forager-1", HiveID: "hive-1"}}
			gate := newTestGate(t, stub, tc.verdicts, store)
			readiness, err := gate.Check(context.Background(), testHive(), 7)
			if err != nil {
				t.Fatal(err)
			}
			if readiness.Ready {
				t.Fatalf("ready = true, want false for %s", tc.name)
			}
			joined := strings.Join(readiness.Reasons, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("reasons = %q, want %q", joined, tc.want)
			}
		})
	}
}

func TestGateNoRequiredChecksFallback(t *testing.T) {
	cases := []struct {
		name    string
		checks  []any
		ready   bool
		reasons string
	}{
		{"all passed", []any{successCheck("build", "success"), successCheck("lint", "skipped")}, true, ""},
		{"failure", []any{successCheck("build", "success"), successCheck("lint", "failure")}, false, "failed"},
		{"pending", []any{map[string]any{"id": 2, "name": "build", "head_sha": "head", "status": "in_progress", "app": map[string]any{"id": 7}}}, false, "still in_progress"},
		{"none", []any{}, false, "no check runs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &gitHubStub{pull: testPull(), required: nil, checks: tc.checks}
			gate := newTestGate(t, stub, fakeVerdicts{latest: passVerdict()}, &fakeStore{hive: testHive()})
			readiness, err := gate.Check(context.Background(), testHive(), 7)
			if err != nil {
				t.Fatal(err)
			}
			if readiness.Ready != tc.ready {
				t.Fatalf("ready = %v, reasons = %v", readiness.Ready, readiness.Reasons)
			}
			if tc.reasons != "" && !strings.Contains(strings.Join(readiness.Reasons, "\n"), tc.reasons) {
				t.Fatalf("reasons = %v, want %q", readiness.Reasons, tc.reasons)
			}
		})
	}
}

func TestGateConflictNotifiesOwnerOnce(t *testing.T) {
	stub := &gitHubStub{
		pull:     testPull(),
		required: []any{map[string]any{"context": "test", "app_id": 7}},
		checks:   []any{successCheck("test", "success")},
	}
	stub.pull["mergeable"] = false
	stub.pull["mergeable_state"] = "dirty"
	store := &fakeStore{
		hive:  testHive(),
		owner: domain.Agent{ID: "forager-1", HiveID: "hive-1", ChainID: "chain-1"},
	}
	gate := newTestGate(t, stub, fakeVerdicts{latest: passVerdict()}, store)
	for range 2 {
		if _, err := gate.Check(context.Background(), testHive(), 7); err != nil {
			t.Fatal(err)
		}
	}
	messages := store.sent()
	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want one idempotent conflict notice", messages)
	}
	message := messages[0]
	if message.To != domain.ForagerAddress("forager-1") || message.Kind != domain.MessageTask {
		t.Fatalf("message = %+v", message)
	}
	if !strings.Contains(message.Body, "冲突") {
		t.Fatalf("body = %q", message.Body)
	}
}

func TestMergeQueueReportsQueued(t *testing.T) {
	stub := &gitHubStub{
		pull:     testPull(),
		required: []any{map[string]any{"context": "test", "app_id": 7}},
		checks:   []any{successCheck("test", "success")},
		rules:    []any{map[string]any{"type": "merge_queue"}},
	}
	stub.queueEntry = map[string]any{"id": "entry"}
	store := &fakeStore{hive: testHive()}
	gate := newTestGate(t, stub, fakeVerdicts{latest: passVerdict()}, store)
	outcome, err := gate.Merge(context.Background(), testHive(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Queued || outcome.Merged || outcome.QueueEntryID != "entry" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.HeadSHA != "head" || stub.mergeSHA != "" {
		t.Fatalf("outcome = %+v, mergeSHA = %q", outcome, stub.mergeSHA)
	}
}

func TestMergePinsHeadSHAAndReportsGitHubRefusal(t *testing.T) {
	stub := &gitHubStub{
		pull:          testPull(),
		required:      []any{map[string]any{"context": "test", "app_id": 7}},
		checks:        []any{successCheck("test", "success")},
		mergeResponse: map[string]any{"merged": true},
	}
	gate := newTestGate(t, stub, fakeVerdicts{latest: passVerdict()}, &fakeStore{hive: testHive()})
	outcome, err := gate.Merge(context.Background(), testHive(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Merged || outcome.Queued || outcome.Method != "squash" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if !stub.mergeRequested || stub.mergeSHA != "head" || stub.mergeMethod != "squash" {
		t.Fatalf("merge request = %+v sha=%q method=%q", stub.mergeRequested, stub.mergeSHA, stub.mergeMethod)
	}

	refused := &gitHubStub{
		pull:        testPull(),
		required:    []any{map[string]any{"context": "test", "app_id": 7}},
		checks:      []any{successCheck("test", "success")},
		mergeStatus: http.StatusMethodNotAllowed,
	}
	gate = newTestGate(t, refused, fakeVerdicts{latest: passVerdict()}, &fakeStore{hive: testHive()})
	if _, err := gate.Merge(context.Background(), testHive(), 7); err == nil || !strings.Contains(err.Error(), "405") {
		t.Fatalf("err = %v, want GitHub refusal with status", err)
	}
}

func TestMergeRefusesStaleVerdict(t *testing.T) {
	stub := &gitHubStub{
		pull:     testPull(),
		required: []any{map[string]any{"context": "test", "app_id": 7}},
		checks:   []any{successCheck("test", "success")},
	}
	stale := passVerdict()
	stale.HeadSHA = "old"
	gate := newTestGate(t, stub, fakeVerdicts{latest: stale}, &fakeStore{hive: testHive()})
	_, err := gate.Merge(context.Background(), testHive(), 7)
	var notReady *NotReadyError
	if !errors.As(err, &notReady) {
		t.Fatalf("err = %v, want NotReadyError", err)
	}
	if stub.mergeRequested {
		t.Fatal("stale verdict still merged")
	}
}

func TestGateAcceptsPassNotes(t *testing.T) {
	stub := &gitHubStub{
		pull:     testPull(),
		required: []any{map[string]any{"context": "test", "app_id": 7}},
		checks:   []any{successCheck("test", "success")},
	}
	verdict := passVerdict()
	verdict.Result = domain.VerdictPassNotes
	gate := newTestGate(t, stub, fakeVerdicts{latest: verdict}, &fakeStore{hive: testHive()})
	readiness, err := gate.Check(context.Background(), testHive(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready {
		t.Fatalf("pass_notes was rejected: %+v", readiness.Reasons)
	}
}
