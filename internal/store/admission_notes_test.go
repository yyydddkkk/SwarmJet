package store

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestPendingAndCancelledTaskNotesStayVisible(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTaskScheduled(ctx, testTask("waiting-note", p, 1), true)
	if err != nil || task.AdmissionStatus != "waiting" {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	note := domain.Command{ID: "note-before-cancel", Kind: "note", Text: "保留这条补充说明"}
	for i := 0; i < 2; i++ {
		if err := s.Enqueue(ctx, task.ID, note); err != nil {
			t.Fatal(err)
		}
	}
	if cancelled, err := s.CancelPendingTask(ctx, task.ID); err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	if err := s.Enqueue(ctx, task.ID, domain.Command{ID: "note-after-cancel", Kind: "note", Text: "取消后仍可补充"}); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Task(ctx, task.ID)
	if err != nil || stored.Stage != "cancelled" || len(stored.Notes) != 2 || stored.Notes[0] != note.Text || stored.Notes[1] != "取消后仍可补充" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	pending, err := s.Pending(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("orphaned commands=%+v err=%v", pending, err)
	}
}
