package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
)

func nextStreamEvent(t *testing.T, c *eventClient, kind string) map[string]any {
	t.Helper()
	select {
	case e := <-c.events:
		prefix := "event: " + kind + "\ndata: "
		if !strings.HasPrefix(string(e.frame), prefix) {
			t.Fatalf("wrong event: %s", e.frame)
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(string(e.frame), prefix))), &data); err != nil {
			t.Fatal(err)
		}
		return data
	case <-c.done:
		t.Fatal("stream disconnected")
	case <-time.After(5 * time.Second):
		t.Fatal("event timed out")
	}
	return nil
}

func TestEventsCommittedEntities(t *testing.T) {
	s, _, _ := newHiveAPITest(t)
	t.Cleanup(s.Close)
	ctx := context.Background()
	hive, err := s.CreateHive(ctx, domain.Hive{Name: "events", Repository: "acme/events"})
	if err != nil {
		t.Fatal(err)
	}
	a := &server{s: s, cfg: Config{Merge: fakePRMergeStatus{readiness: merge.Readiness{HeadSHA: "head", Ready: true, Pull: merge.Pull{State: "open"}}}}}
	hub, err := a.eventHub(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := hub.add(hive.ID)
	defer hub.remove(c)
	other := hub.add("another-hive")
	defer hub.remove(other)
	all := hub.add("")
	defer hub.remove(all)
	message, err := s.SendMessage(ctx, domain.Message{HiveID: hive.ID, From: "local:admin", To: domain.AddressHive, Kind: domain.MessageTask, ChainID: "chain", Body: strings.Repeat("星", 121)})
	if err != nil {
		t.Fatal(err)
	}
	m := nextStreamEvent(t, c, "message")
	if m["id"] != message.ID || m["hiveId"] != hive.ID || m["preview"] != strings.Repeat("星", 120) || m["from"] != "local:admin" || m["to"] != "hive" || m["kind"] != "task" || m["trust"] != "user" || m["chainId"] != "chain" {
		t.Fatalf("message: %+v", m)
	}
	if _, err := time.Parse(time.RFC3339Nano, m["createdAt"].(string)); err != nil {
		t.Fatal(err)
	}
	nextStreamEvent(t, all, "message")
	// Updates to a message do not replay it as a new message.
	if err := s.MarkMessageHandled(ctx, message.ID); err != nil {
		t.Fatal(err)
	}
	queen, err := s.Queen(ctx, hive.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE agents SET state='active',pr_number=7,issue_number=9,role_name='worker' WHERE id=$1`, queen.ID); err != nil {
		t.Fatal(err)
	}
	agent := nextStreamEvent(t, c, "agent")
	if agent["id"] != queen.ID || agent["state"] != "active" || agent["prNumber"] != float64(7) || agent["issueNumber"] != float64(9) || agent["roleName"] != "worker" || agent["costUsd"] != float64(0) {
		t.Fatalf("agent: %+v", agent)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE agents SET updated_at=now() WHERE id=$1`, queen.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO attestations(id,hive_id,pr_number,head_sha,attester_id,result) VALUES('att-event',$1,7,'head','verifier','pass')`, hive.ID); err != nil {
		t.Fatal(err)
	}
	pr := nextStreamEvent(t, c, "pr")
	if len(pr) != 6 || pr["hiveId"] != hive.ID || pr["number"] != float64(7) || pr["headSha"] != "head" || pr["state"] != "open" || pr["ready"] != true || pr["merged"] != false {
		t.Fatalf("pr: %+v", pr)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO methodology_events(hive_id,action,version,actor,note) VALUES($1,'activate',2,'system','rollback')`, hive.ID); err != nil {
		t.Fatal(err)
	}
	meth := nextStreamEvent(t, c, "methodology")
	if len(meth) != 5 || meth["version"] != float64(2) || meth["action"] != "activate" || meth["actor"] != "system" || meth["note"] != "rollback" {
		t.Fatalf("methodology: %+v", meth)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO spawn_queue(hive_id,agent_id,agent,task_message,limits) VALUES($1,'queued','{"roleName":"worker"}','{"body":"task"}','{}')`, hive.ID); err != nil {
		t.Fatal(err)
	}
	queue := nextStreamEvent(t, c, "queue")
	if len(queue) != 7 || queue["agentId"] != "queued" || queue["role"] != "worker" || queue["task"] != "task" || queue["position"] != float64(1) || queue["state"] != "waiting" {
		t.Fatalf("queue: %+v", queue)
	}
	for _, state := range []string{"started", "expired"} {
		if _, err = s.Pool.Exec(ctx, `UPDATE spawn_queue SET state=$1 WHERE agent_id='queued'`, state); err != nil {
			t.Fatal(err)
		}
		if got := nextStreamEvent(t, c, "queue"); got["state"] != state {
			t.Fatalf("queue transition: %+v", got)
		}
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE hives SET name='changed' WHERE id=$1`, hive.ID); err != nil {
		t.Fatal(err)
	}
	updated := nextStreamEvent(t, c, "hive")
	if updated["id"] != hive.ID || updated["name"] != "changed" || updated["config"] == nil {
		t.Fatalf("hive: %+v", updated)
	}
	select {
	case e := <-other.events:
		t.Fatalf("hive filter leaked: %s", e.frame)
	default:
	}
	select {
	case e := <-c.events:
		t.Fatalf("unexpected duplicate: %s", e.frame)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestEventHubDisconnectsSlowSubscriber(t *testing.T) {
	h := &eventHub{clients: make(map[*eventClient]struct{}), done: make(chan struct{})}
	slow := h.add("hive")
	fast := h.add("hive")
	defer h.stop()
	for i := 0; i <= eventBufferSize; i++ {
		h.broadcast(streamEvent{hiveID: "hive", frame: []byte("frame")})
		<-fast.events
	}
	select {
	case <-slow.done:
	default:
		t.Fatal("slow subscriber remained connected")
	}
	select {
	case <-fast.done:
		t.Fatal("fast subscriber disconnected")
	default:
	}
	h.broadcast(streamEvent{hiveID: "hive"})
	<-fast.events
}

func TestEventsHTTPReadyAndAuthentication(t *testing.T) {
	s, handler, cookie := newHiveAPITest(t)
	t.Cleanup(s.Close)
	if w := hiveDo(t, handler, nil, http.MethodGet, "/api/events", nil); w.Code != 401 {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet, "/api/events?hive=missing", nil); w.Code != 404 {
		t.Fatalf("missing hive: %d", w.Code)
	}
	a := &server{s: s, cfg: Config{}}
	server := httptest.NewServer(http.HandlerFunc(a.serve))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/events", nil)
	req.AddCookie(cookie)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("SSE response: %d %v", response.StatusCode, response.Header)
	}
	reader := bufio.NewReader(response.Body)
	for _, want := range []string{"event: ready\n", "data: {}\n", "\n"} {
		got, err := reader.ReadString('\n')
		if err != nil || got != want {
			t.Fatalf("ready: %q %v", got, err)
		}
	}
	response.Body.Close()
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		a.events.mu.Lock()
		active := len(a.events.clients)
		a.events.mu.Unlock()
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected client retained")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestEventsOverrideServerWriteTimeout(t *testing.T) {
	s, _, cookie := newHiveAPITest(t)
	t.Cleanup(s.Close)
	hive, err := s.CreateHive(context.Background(), domain.Hive{Name: "timeout", Repository: "acme/timeout"})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(s, Config{})
	server := httptest.NewUnstartedServer(handler)
	server.Config.WriteTimeout = 20 * time.Millisecond
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/events?hive="+hive.ID, nil)
	req.AddCookie(cookie)
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	for i := 0; i < 3; i++ {
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}
	// A real network connection must remain writable beyond the server's deadline.
	time.Sleep(80 * time.Millisecond)
	message, err := s.SendMessage(ctx, domain.Message{HiveID: hive.ID, From: "local:admin", To: domain.AddressHive, Kind: domain.MessageTask, Body: "after deadline"})
	if err != nil {
		t.Fatal(err)
	}
	line, err := reader.ReadString('\n')
	if err != nil || line != "event: message\n" {
		t.Fatalf("event after timeout: %q %v", line, err)
	}
	line, err = reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data: "))), &got); err != nil || got["id"] != message.ID {
		t.Fatalf("data=%+v err=%v", got, err)
	}
}

type blockingEventMergeStatus struct {
	started  chan struct{}
	canceled chan struct{}
	calls    atomic.Int32
}

func (b *blockingEventMergeStatus) Check(ctx context.Context, _ domain.Hive, _ int64) (merge.Readiness, error) {
	if b.calls.Add(1) == 1 {
		close(b.started)
	}
	<-ctx.Done()
	close(b.canceled)
	return merge.Readiness{}, ctx.Err()
}
func (*blockingEventMergeStatus) Attestations(context.Context, string, int64) ([]domain.Attestation, error) {
	return nil, nil
}

func TestEventsCancelBackloggedHydration(t *testing.T) {
	for _, cause := range []string{"disconnect", "overflow"} {
		t.Run(cause, func(t *testing.T) {
			s, _, cookie := newHiveAPITest(t)
			t.Cleanup(s.Close)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			hive, err := s.CreateHive(ctx, domain.Hive{Name: "backlog", Repository: "acme/backlog"})
			if err != nil {
				t.Fatal(err)
			}
			blocked := &blockingEventMergeStatus{started: make(chan struct{}), canceled: make(chan struct{})}
			a := &server{s: s, cfg: Config{Merge: blocked}}
			server := httptest.NewServer(http.HandlerFunc(a.serve))
			defer server.Close()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/events?hive="+hive.ID, nil)
			req.AddCookie(cookie)
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			reader := bufio.NewReader(response.Body)
			for i := 0; i < 3; i++ {
				if _, err := reader.ReadString('\n'); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.PublishPREvent(ctx, hive.ID, 7); err != nil {
				t.Fatal(err)
			}
			select {
			case <-blocked.started:
			case <-ctx.Done():
				t.Fatal("hydration did not start")
			}
			count := 32
			if cause == "overflow" {
				count = 300
			}
			if _, err := s.Pool.Exec(ctx, `SELECT pg_notify('swarmjet_events',json_build_object('type','pr','hiveId',$1::text,'id',n::text)::text) FROM generate_series(1,$2::integer) n`, hive.ID, count); err != nil {
				t.Fatal(err)
			}
			if cause == "disconnect" {
				s.Close()
			}
			select {
			case <-blocked.canceled:
			case <-time.After(time.Second):
				t.Fatal("listener termination did not cancel hydration")
			}
			// The same real HTTP connection ends promptly; stale buffered refs never
			// reach another Check and the watcher exits even while hydration unwinds.
			if _, err := reader.ReadString('\n'); err != io.EOF {
				t.Fatalf("HTTP stream did not close: %v", err)
			}
			if blocked.calls.Load() != 1 {
				t.Fatalf("drained stale events: %d calls", blocked.calls.Load())
			}
			a.events.mu.Lock()
			active := len(a.events.clients)
			a.events.mu.Unlock()
			if active != 0 {
				t.Fatalf("retained %d HTTP subscribers", active)
			}
		})
	}
}
