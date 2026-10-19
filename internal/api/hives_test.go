package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// hiveTestJobs is the in-memory delivery queue for the API tests. The store
// refuses to write messages without one, and this fake only records the IDs.
type hiveTestJobs struct {
	mu       sync.Mutex
	messages []string
}

func (j *hiveTestJobs) InsertWebhook(context.Context, pgx.Tx, jobs.Webhook) error { return nil }
func (j *hiveTestJobs) InsertCommand(context.Context, pgx.Tx, jobs.Command) error { return nil }

func (j *hiveTestJobs) InsertMessage(_ context.Context, _ pgx.Tx, job jobs.Message) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.messages = append(j.messages, job.ID)
	return nil
}

func (j *hiveTestJobs) ids() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.messages...)
}

// newHiveAPITest creates an isolated schema and returns a server signed in as
// the breakglass admin. The tests exercise the real HTTP stack and real store.
func newHiveAPITest(t *testing.T) (*store.Store, http.Handler, *http.Cookie) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	root, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	schema := "api_hives_" + strings.ReplaceAll(id, "-", "")
	if _, err = root.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = root.Pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	config := root.Pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := &store.Store{Pool: pool}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	s.Jobs = &hiveTestJobs{}
	handler := New(s, Config{
		Username:      "admin",
		Password:      "secret",
		AllowedOrigin: "https://console.test",
		SecureCookies: true,
	})
	cookie := hiveTestLogin(t, handler)
	return s, handler, cookie
}

// hiveTestUserCookie creates an active, non-admin user with a real session.
func hiveTestUserCookie(t *testing.T, s *store.Store) *http.Cookie {
	t.Helper()
	ctx := context.Background()
	userID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO users(id,email,display_name,password_hash,status) VALUES($1,'viewer@example.test','Viewer','hash','active')`, userID); err != nil {
		t.Fatal(err)
	}
	token, err := opaqueToken()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddUserSession(ctx, tokenHash(token), userID); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "swarmjet_session", Value: token}
}

func hiveTestLogin(t *testing.T, handler http.Handler) *http.Cookie {
	t.Helper()
	r := httptest.NewRequest("POST", "https://console.test/api/login", strings.NewReader(`{"username":"admin","password":"secret"}`))
	r.Header.Set("Origin", "https://console.test")
	r.Header.Set("X-Requested-With", "swarmjet")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies: %+v", cookies)
	}
	return cookies[0]
}

func hiveDo(t *testing.T, handler http.Handler, cookie *http.Cookie, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	r := httptest.NewRequest(method, "https://console.test"+path, reader)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if method != http.MethodGet && method != http.MethodHead {
		r.Header.Set("Origin", "https://console.test")
		r.Header.Set("X-Requested-With", "swarmjet")
		if body != nil {
			r.Header.Set("Content-Type", "application/json")
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func hiveDecode[T any](t *testing.T, w *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	var value T
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	return value
}

type createHiveResponse struct {
	Hive         domain.Hive `json:"hive"`
	LocalAddress string      `json:"localAddress"`
}

type listHivesResponse struct {
	Hives        []hiveOverview `json:"hives"`
	LocalAddress string         `json:"localAddress"`
	Truncated    bool           `json:"truncated"`
}

type listForagersResponse struct {
	Queen        *domain.Agent  `json:"queen"`
	Foragers     []domain.Agent `json:"foragers"`
	LocalAddress string         `json:"localAddress"`
	Truncated    bool           `json:"truncated"`
}

type spawnForagerResponse struct {
	Forager domain.Agent   `json:"forager"`
	Message domain.Message `json:"message"`
}

type foragerResponse struct {
	Forager domain.Agent `json:"forager"`
	Hive    domain.Hive  `json:"hive"`
}

type messagePageResponse struct {
	Messages  []domain.Message `json:"messages"`
	Cursor    string           `json:"cursor"`
	Truncated bool             `json:"truncated"`
	To        string           `json:"to"`
}

type sendMessageResponse struct {
	Message domain.Message `json:"message"`
}

func createHiveForTest(t *testing.T, handler http.Handler, cookie *http.Cookie, body createHiveRequest) domain.Hive {
	t.Helper()
	return hiveDecode[createHiveResponse](t, hiveDo(t, handler, cookie, http.MethodPost, "/api/hives", body), http.StatusCreated).Hive
}

func TestHiveAPIAuthentication(t *testing.T) {
	_, handler, _ := newHiveAPITest(t)
	if w := hiveDo(t, handler, nil, http.MethodGet, "/api/hives", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list: %d", w.Code)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "https://console.test/api/hives", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://console.test")
	r.Header.Set("X-Requested-With", "swarmjet")
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create: %d", w.Code)
	}
}

func TestHiveAPIPlatformAdminOnly(t *testing.T) {
	s, handler, admin := newHiveAPITest(t)
	hive := createHiveForTest(t, handler, admin, createHiveRequest{Name: "Admin only", Repository: "owner/admin-only"})
	spawned := hiveDecode[spawnForagerResponse](t, hiveDo(t, handler, admin, http.MethodPost, "/api/hives/"+hive.ID+"/foragers", spawnForagerRequest{
		Task: "admin work",
	}), http.StatusCreated)
	user := hiveTestUserCookie(t, s)

	forbidden := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/hives", nil},
		{http.MethodPost, "/api/hives", createHiveRequest{Name: "user", Repository: "owner/user"}},
		{http.MethodGet, "/api/hives/" + hive.ID, nil},
		{http.MethodGet, "/api/hives/" + hive.ID + "/foragers", nil},
		{http.MethodPost, "/api/hives/" + hive.ID + "/foragers", spawnForagerRequest{Task: "user work"}},
		{http.MethodGet, "/api/foragers/" + spawned.Forager.ID, nil},
		{http.MethodPost, "/api/foragers/" + spawned.Forager.ID + "/cancel", nil},
		{http.MethodGet, "/api/hives/" + hive.ID + "/messages?to=queen", nil},
		{http.MethodPost, "/api/hives/" + hive.ID + "/messages", sendMessageRequest{To: "queen", Body: "user message"}},
	}
	for _, testCase := range forbidden {
		w := hiveDo(t, handler, user, testCase.method, testCase.path, testCase.body)
		if w.Code != http.StatusForbidden {
			t.Fatalf("non-admin %s %s: %d %s", testCase.method, testCase.path, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "platform administrator required") {
			t.Fatalf("non-admin %s %s: %s", testCase.method, testCase.path, w.Body.String())
		}
	}

	for _, path := range []string{
		"/api/hives",
		"/api/hives/" + hive.ID,
		"/api/hives/" + hive.ID + "/foragers",
		"/api/foragers/" + spawned.Forager.ID,
		"/api/hives/" + hive.ID + "/messages?to=queen",
	} {
		if w := hiveDo(t, handler, admin, http.MethodGet, path, nil); w.Code != http.StatusOK {
			t.Fatalf("admin GET %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := hiveDo(t, handler, admin, http.MethodPost, "/api/hives/"+hive.ID+"/foragers", spawnForagerRequest{Task: "another admin spawn"}); w.Code != http.StatusCreated {
		t.Fatalf("admin spawn: %d %s", w.Code, w.Body.String())
	}
}

func TestHiveAPIHiveLifecycle(t *testing.T) {
	_, handler, cookie := newHiveAPITest(t)

	created := createHiveForTest(t, handler, cookie, createHiveRequest{
		Name:       "SwarmJet",
		Repository: "yyydddkkk/SwarmJet",
		Config:     &domain.HiveConfig{MaxConcurrentForagers: 2},
	})
	if created.ID == "" || created.Version != 1 || created.QueenEnabled {
		t.Fatalf("created hive: %+v", created)
	}
	if created.Config.CLI != "opencode" || created.Config.MaxHops != 10 {
		t.Fatalf("defaults not applied: %+v", created.Config)
	}

	if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/hives", createHiveRequest{
		Name: "duplicate", Repository: "yyydddkkk/SwarmJet",
	}); w.Code != http.StatusConflict {
		t.Fatalf("duplicate repository: %d %s", w.Code, w.Body.String())
	}
	for _, body := range []createHiveRequest{
		{Name: "bad repo", Repository: "not a repository"},
		{Name: "", Repository: "owner/name"},
	} {
		if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/hives", body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid hive %+v: %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/hives", createHiveRequest{
		Name: "negative", Repository: "owner/negative",
		Config: &domain.HiveConfig{IdleParkMinutes: -1},
	}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("negative limit: %d %s", w.Code, w.Body.String())
	}
	if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/hives", createHiveRequest{
		Name: "trigger", Repository: "owner/trigger",
		Config: &domain.HiveConfig{Triggers: []domain.TriggerRule{{Event: "issue_labeled"}}},
	}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid trigger: %d %s", w.Code, w.Body.String())
	}

	if got := hiveDecode[createHiveResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+created.ID, nil), http.StatusOK); got.Hive.ID != created.ID {
		t.Fatalf("get hive: %+v", got.Hive)
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/missing", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing hive: %d", w.Code)
	}

	second := createHiveForTest(t, handler, cookie, createHiveRequest{Name: "Second", Repository: "owner/second"})
	list := hiveDecode[listHivesResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives", nil), http.StatusOK)
	if len(list.Hives) != 2 || list.Hives[0].Hive.ID != created.ID || list.Hives[1].Hive.ID != second.ID {
		t.Fatalf("list order: %+v", list.Hives)
	}
	if list.LocalAddress != "local:web-admin" {
		t.Fatalf("local address: %q", list.LocalAddress)
	}

	enabled := createHiveForTest(t, handler, cookie, createHiveRequest{
		Name: "Queen on", Repository: "owner/queen-on", QueenEnabled: true,
	})
	agents := hiveDecode[listForagersResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+enabled.ID+"/foragers", nil), http.StatusOK)
	if agents.Queen == nil || agents.Queen.State != domain.AgentActive {
		t.Fatalf("enabled queen: %+v", agents.Queen)
	}
}

func TestHiveAPIForagers(t *testing.T) {
	s, handler, cookie := newHiveAPITest(t)
	hive := createHiveForTest(t, handler, cookie, createHiveRequest{Name: "Swarm", Repository: "owner/swarm"})

	spawned := hiveDecode[spawnForagerResponse](t, hiveDo(t, handler, cookie, http.MethodPost, "/api/hives/"+hive.ID+"/foragers", spawnForagerRequest{
		Task: "Fix the failing test", Preset: "implementation",
	}), http.StatusCreated)
	if spawned.Forager.ID == "" || spawned.Forager.Role != domain.RoleForager || spawned.Forager.State != domain.AgentCreating {
		t.Fatalf("spawned forager: %+v", spawned.Forager)
	}
	if !strings.HasPrefix(spawned.Forager.Branch, "swarm/"+hive.ID+"/") {
		t.Fatalf("branch: %q", spawned.Forager.Branch)
	}
	if spawned.Message.Kind != domain.MessageTask || spawned.Message.To != domain.ForagerAddress(spawned.Forager.ID) || spawned.Message.Body != "Fix the failing test" {
		t.Fatalf("task message: %+v", spawned.Message)
	}
	if spawned.Message.From != "local:web-admin" {
		t.Fatalf("task sender: %q", spawned.Message.From)
	}

	agents := hiveDecode[listForagersResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/foragers", nil), http.StatusOK)
	if agents.Queen == nil || len(agents.Foragers) != 1 || agents.Foragers[0].ID != spawned.Forager.ID {
		t.Fatalf("forager list: %+v", agents)
	}
	listed := hiveDecode[listHivesResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives", nil), http.StatusOK)
	if len(listed.Hives) != 1 || listed.Hives[0].ActiveForagers != 1 {
		t.Fatalf("overview active foragers: %+v", listed.Hives)
	}
	detail := hiveDecode[foragerResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/foragers/"+spawned.Forager.ID, nil), http.StatusOK)
	if detail.Forager.ID != spawned.Forager.ID || detail.Hive.ID != hive.ID {
		t.Fatalf("forager detail: %+v", detail)
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet, "/api/foragers/missing", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing forager: %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet, "/api/foragers/"+agents.Queen.ID, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("queen via forager route: %d", w.Code)
	}

	if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/hives/"+hive.ID+"/foragers", spawnForagerRequest{Task: "  "}); w.Code != http.StatusBadRequest {
		t.Fatalf("empty task: %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/hives/"+hive.ID+"/foragers", spawnForagerRequest{
		Task: strings.Repeat("x", domain.MaxMessageBody+1),
	}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("oversized task: %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/hives/missing/foragers", spawnForagerRequest{Task: "x"}); w.Code != http.StatusNotFound {
		t.Fatalf("spawn into missing hive: %d", w.Code)
	}

	cancelled := hiveDecode[sendMessageResponse](t, hiveDo(t, handler, cookie, http.MethodPost, "/api/foragers/"+spawned.Forager.ID+"/cancel", nil), http.StatusCreated)
	if cancelled.Message.Kind != domain.MessageControl || cancelled.Message.Body != "cancel" {
		t.Fatalf("cancel message: %+v", cancelled.Message)
	}
	inbox := hiveDecode[messagePageResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/messages?to=forager:"+spawned.Forager.ID, nil), http.StatusOK)
	if len(inbox.Messages) != 2 || inbox.Messages[1].Kind != domain.MessageControl {
		t.Fatalf("forager inbox: %+v", inbox.Messages)
	}

	if _, err := s.Pool.Exec(context.Background(), `UPDATE agents SET state='completed' WHERE id=$1`, spawned.Forager.ID); err != nil {
		t.Fatal(err)
	}
	if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/foragers/"+spawned.Forager.ID+"/cancel", nil); w.Code != http.StatusConflict {
		t.Fatalf("cancel terminal forager: %d %s", w.Code, w.Body.String())
	}

	limited := createHiveForTest(t, handler, cookie, createHiveRequest{
		Name: "One at a time", Repository: "owner/limited",
		Config: &domain.HiveConfig{MaxConcurrentForagers: 1},
	})
	if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/hives/"+limited.ID+"/foragers", spawnForagerRequest{Task: "first"}); w.Code != http.StatusCreated {
		t.Fatalf("first spawn: %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodPost, "/api/hives/"+limited.ID+"/foragers", spawnForagerRequest{Task: "second"}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrency limit: %d %s", w.Code, w.Body.String())
	}
}

func TestHiveAPIMessages(t *testing.T) {
	s, handler, cookie := newHiveAPITest(t)
	hive := createHiveForTest(t, handler, cookie, createHiveRequest{Name: "Messages", Repository: "owner/messages"})
	spawned := hiveDecode[spawnForagerResponse](t, hiveDo(t, handler, cookie, http.MethodPost, "/api/hives/"+hive.ID+"/foragers", spawnForagerRequest{
		Task: "do the work",
	}), http.StatusCreated)

	sent := hiveDecode[sendMessageResponse](t, hiveDo(t, handler, cookie, http.MethodPost, "/api/hives/"+hive.ID+"/messages", sendMessageRequest{
		To: "queen", Body: "hello queen",
	}), http.StatusCreated)
	if sent.Message.From != "local:web-admin" || sent.Message.To != domain.AddressQueen || sent.Message.Kind != domain.MessageTask {
		t.Fatalf("sent message: %+v", sent.Message)
	}

	page := hiveDecode[messagePageResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/messages?to=queen", nil), http.StatusOK)
	if len(page.Messages) != 1 || page.Messages[0].ID != sent.Message.ID || page.Cursor != sent.Message.ID || page.To != "queen" {
		t.Fatalf("queen inbox: %+v", page)
	}
	if after := hiveDecode[messagePageResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/messages?to=queen&after="+sent.Message.ID, nil), http.StatusOK); len(after.Messages) != 0 {
		t.Fatalf("after cursor: %+v", after.Messages)
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/messages?to=queen&after=missing", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing cursor: %d", w.Code)
	}

	// A Queen reply lands in the browser user's own local inbox.
	if _, err := s.SendMessage(context.Background(), domain.Message{
		HiveID: hive.ID, From: domain.AddressQueen, To: "local:web-admin", Kind: domain.MessageReply, Body: "hello back",
	}); err != nil {
		t.Fatal(err)
	}
	own := hiveDecode[messagePageResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/messages?to=local:web-admin", nil), http.StatusOK)
	if len(own.Messages) != 1 || own.Messages[0].Body != "hello back" {
		t.Fatalf("local inbox: %+v", own.Messages)
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/messages?to=local:web-someone-else", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("other local inbox: %d", w.Code)
	}

	hiveMsg := hiveDecode[sendMessageResponse](t, hiveDo(t, handler, cookie, http.MethodPost, "/api/hives/"+hive.ID+"/messages", sendMessageRequest{
		To: "hive", Kind: "event", Body: "external event",
	}), http.StatusCreated)
	queue, ok := s.Jobs.(*hiveTestJobs)
	if !ok || len(queue.ids()) == 0 {
		t.Fatalf("delivery queue did not receive any message")
	}
	if hiveMsg.Message.Kind != domain.MessageEvent {
		t.Fatalf("hive message: %+v", hiveMsg.Message)
	}
	backlog := hiveDecode[messagePageResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/messages?to=hive", nil), http.StatusOK)
	if len(backlog.Messages) != 1 {
		t.Fatalf("hive inbox: %+v", backlog.Messages)
	}
	listed := hiveDecode[listHivesResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives", nil), http.StatusOK)
	if len(listed.Hives) != 1 || listed.Hives[0].InboxBacklog != 1 {
		t.Fatalf("overview inbox backlog: %+v", listed.Hives)
	}

	if _, err := s.SendMessage(context.Background(), domain.Message{
		HiveID: hive.ID, From: "local:web-admin", To: domain.ForagerAddress(spawned.Forager.ID), Kind: domain.MessageQuestion, Body: "status?",
	}); err != nil {
		t.Fatal(err)
	}
	foragerInbox := hiveDecode[messagePageResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/messages?to=forager:"+spawned.Forager.ID, nil), http.StatusOK)
	if len(foragerInbox.Messages) != 2 {
		t.Fatalf("forager inbox: %+v", foragerInbox.Messages)
	}
	other := createHiveForTest(t, handler, cookie, createHiveRequest{Name: "Other", Repository: "owner/other"})
	otherForager := hiveDecode[spawnForagerResponse](t, hiveDo(t, handler, cookie, http.MethodPost, "/api/hives/"+other.ID+"/foragers", spawnForagerRequest{Task: "x"}), http.StatusCreated)
	if w := hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/messages?to=forager:"+otherForager.Forager.ID, nil); w.Code != http.StatusNotFound {
		t.Fatalf("cross-hive forager inbox: %d", w.Code)
	}

	for _, testCase := range []struct {
		method string
		path   string
		body   any
		want   int
	}{
		{http.MethodGet, "/api/hives/" + hive.ID + "/messages", nil, http.StatusBadRequest},
		{http.MethodGet, "/api/hives/" + hive.ID + "/messages?to=nowhere", nil, http.StatusBadRequest},
		{http.MethodGet, "/api/hives/" + hive.ID + "/messages?to=queen&limit=0", nil, http.StatusBadRequest},
		{http.MethodPost, "/api/hives/" + hive.ID + "/messages", sendMessageRequest{To: "queen"}, http.StatusBadRequest},
		{http.MethodPost, "/api/hives/" + hive.ID + "/messages", sendMessageRequest{To: "nowhere", Body: "x"}, http.StatusBadRequest},
		{http.MethodPost, "/api/hives/" + hive.ID + "/messages", sendMessageRequest{To: "queen", Body: "x", Kind: "invalid"}, http.StatusBadRequest},
		{http.MethodPost, "/api/hives/" + hive.ID + "/messages", sendMessageRequest{To: "queen", Body: strings.Repeat("x", domain.MaxMessageBody+1)}, http.StatusUnprocessableEntity},
	} {
		if w := hiveDo(t, handler, cookie, testCase.method, testCase.path, testCase.body); w.Code != testCase.want {
			t.Fatalf("%s %s: %d, want %d (%s)", testCase.method, testCase.path, w.Code, testCase.want, w.Body.String())
		}
	}

	// Defensive truncation: a body written outside SendMessage still comes back
	// at the domain limit.
	longBody := strings.Repeat("蜜", domain.MaxMessageBody+500)
	if _, err := s.Pool.Exec(context.Background(), `INSERT INTO messages(id,hive_id,from_addr,to_addr,kind,body,state) VALUES('long-message',$1,'system','hive','event',$2,'pending')`, hive.ID, longBody); err != nil {
		t.Fatal(err)
	}
	clamped := hiveDecode[messagePageResponse](t, hiveDo(t, handler, cookie, http.MethodGet, "/api/hives/"+hive.ID+"/messages?to=hive&limit=10", nil), http.StatusOK)
	var found *domain.Message
	for i := range clamped.Messages {
		if clamped.Messages[i].ID == "long-message" {
			found = &clamped.Messages[i]
		}
	}
	if found == nil || utf8.RuneCountInString(found.Body) != domain.MaxMessageBody {
		t.Fatalf("body not truncated: %+v", found)
	}
}
