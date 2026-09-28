package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"swarmjet/internal/domain"
)

func TestRunCommentRequiresExactCommand(t *testing.T) {
	for _, test := range []struct {
		body, want string
		ok         bool
	}{
		{"/swarmjet run 修复反馈", "修复反馈", true},
		{" /swarmjet run\n补充检查 ", "补充检查", true},
		{"/swarmjet run", "", true},
		{"/swarmjet runner", "", false},
		{"已通过", "", false},
		{"<!-- swarmjet-progress:x -->\n/swarmjet run", "", false},
	} {
		got, ok := runComment(test.body)
		if got != test.want || ok != test.ok {
			t.Fatalf("body=%q got=%q,%v", test.body, got, ok)
		}
	}
}

func TestFollowupCommandsQueueOnceAfterCurrentRound(t *testing.T) {
	s := scanTestStore(t)
	ctx := context.Background()
	created := time.Now().UTC().Add(time.Minute)
	note := map[string]any{"id": 81, "body": "/swarmjet run 修复复核反馈", "system": false, "created_at": created, "updated_at": created, "author": map[string]any{"id": 42}}
	extraNotes := []any{
		map[string]any{"id": 85, "body": "/swarmjet run 增加回归检查", "created_at": created.Add(time.Millisecond), "updated_at": created.Add(time.Millisecond), "author": map[string]any{"id": 42}},
		map[string]any{"id": 82, "body": "/swarmjet run 编辑后补上", "created_at": created, "updated_at": created.Add(time.Second)},
		map[string]any{"id": 83, "body": "/swarmjet run 私密信息", "internal": true, "created_at": created, "updated_at": created},
		map[string]any{"id": 84, "body": "/swarmjet run 系统信息", "system": true, "created_at": created, "updated_at": created},
	}
	gitlab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeScanLabelEvent(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v4/projects/7/issues/1/notes" {
			_ = json.NewEncoder(w).Encode(append([]any{note}, extraNotes...))
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{"project_id": 7, "iid": 1, "title": "task", "state": "opened", "labels": []string{"autonomous"}, "web_url": "http://example/7/1"}})
	}))
	defer gitlab.Close()
	p := scanProject(gitlab.URL)
	p.Nodes = domain.NodeSelection{Mode: "auto"}
	if _, err := s.SaveProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	grantScanActor(t, s, p)
	svc := &Service{Store: s, ResolveCredential: func(string) (string, error) { return "token", nil }}
	runConcurrent := func() {
		var scans sync.WaitGroup
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			scans.Add(1)
			go func() { defer scans.Done(); results <- svc.Scan(ctx) }()
		}
		scans.Wait()
		close(results)
		for err := range results {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	runConcurrent()
	tasks, err := s.Tasks(ctx)
	if err != nil || len(tasks) != 1 || tasks[0].Round != 1 {
		t.Fatalf("first tasks=%+v err=%v", tasks, err)
	}
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err = s.Tasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("active round duplicated: %+v %v", tasks, err)
	}
	first := tasks[0]
	first.Stage = "completed"
	if err := s.SaveTask(ctx, first); err != nil {
		t.Fatal(err)
	}
	runConcurrent()
	tasks, err = s.Tasks(ctx)
	if err != nil || len(tasks) != 2 || tasks[0].Round != 2 || tasks[0].PreviousTaskID != first.ID || tasks[0].TriggerText != "修复复核反馈\n\n增加回归检查" || len(tasks[0].TriggerNoteIDs) != 2 {
		t.Fatalf("follow-up tasks=%+v err=%v", tasks, err)
	}
	if err := svc.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	tasks, err = s.Tasks(ctx)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("repeat scan duplicated round: %+v %v", tasks, err)
	}
}
