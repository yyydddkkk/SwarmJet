package store

import (
	"context"
	"testing"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestCommandPublicationResendAndStartGate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	project, err := s.SaveProject(ctx, testProject())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTask(ctx, testTask("resend", project, 42)); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, "resend", domain.Command{ID: "note-resend", Kind: "note", Text: "once"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.CommandsForPublication(ctx, time.Minute)
	if err != nil || len(list) != 1 || list[0].Command.ID != "start-resend" {
		t.Fatalf("only start should publish first: %+v %v", list, err)
	}
	if err := s.MarkCommandPublished(ctx, "start-resend"); err != nil {
		t.Fatal(err)
	}
	list, err = s.CommandsForPublication(ctx, time.Minute)
	if err != nil || len(list) != 0 {
		t.Fatalf("confirmed publication resent too early: %+v %v", list, err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE commands SET published_at=now()-interval '2 minutes' WHERE id='start-resend'`); err != nil {
		t.Fatal(err)
	}
	list, err = s.CommandsForPublication(ctx, time.Minute)
	if err != nil || len(list) != 1 || list[0].Command.ID != "start-resend" {
		t.Fatalf("stale publication not resent: %+v %v", list, err)
	}
	if err := s.Delivered(ctx, "start-resend"); err != nil {
		t.Fatal(err)
	}
	list, err = s.CommandsForPublication(ctx, time.Minute)
	if err != nil || len(list) != 1 || list[0].Command.ID != "note-resend" {
		t.Fatalf("note should follow confirmed start delivery: %+v %v", list, err)
	}
	item, delivered, err := s.CommandForDelivery(ctx, "note-resend")
	if err != nil || delivered || item.TaskID != "resend" || item.Command.Text != "once" {
		t.Fatalf("command lookup: %+v delivered=%v err=%v", item, delivered, err)
	}
}
