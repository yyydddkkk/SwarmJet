package store

import (
	"context"
	"fmt"
	"testing"
)

func TestTaskPageAndGitLabSyncReachBeyondRecentTwoHundred(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 205; i++ {
		task := testTask(fmt.Sprintf("task-%03d", i), p, int64(i))
		task.Issue.Title = fmt.Sprintf("issue %d", i)
		if i == 1 {
			task.Issue.Title = "unique search target"
		}
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		// The paging fixture models historical work, outside admission limits.
		task.Stage = "completed"
		if err := s.SaveTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.TaskPage(ctx, TaskPageOptions{Limit: 50, View: "all"})
	if err != nil || len(page.Items) != 50 || page.Total != 205 || page.Counts.All != 205 || page.NextCursor == "" {
		t.Fatalf("first page=%+v err=%v", page, err)
	}
	second, err := s.TaskPage(ctx, TaskPageOptions{Limit: 50, View: "all", Cursor: page.NextCursor})
	if err != nil || len(second.Items) != 50 || second.Items[0].ID == page.Items[0].ID {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	searched, err := s.TaskPage(ctx, TaskPageOptions{Search: "unique search target", View: "all"})
	if err != nil || searched.Total != 1 || searched.Items[0].ID != "task-001" {
		t.Fatalf("search=%+v err=%v", searched, err)
	}
	processed := 0
	for processed < 205 {
		batch, err := s.PendingGitLabSync(ctx)
		if err != nil || len(batch) == 0 {
			t.Fatalf("pending batch=%d err=%v", len(batch), err)
		}
		for _, item := range batch {
			if err := s.CompleteGitLabSync(ctx, item.Task.ID, item.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			processed++
		}
	}
	remaining, err := s.PendingGitLabSync(ctx)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining=%d err=%v", len(remaining), err)
	}
}
