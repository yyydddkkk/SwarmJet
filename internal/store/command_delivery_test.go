package store

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestCommandForDeliveryReadsUndeliveredCommand(t *testing.T) {
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
	item, delivered, err := s.CommandForDelivery(ctx, "note-resend")
	if err != nil || delivered || item.TaskID != "resend" || item.Command.Text != "once" {
		t.Fatalf("command lookup: %+v delivered=%v err=%v", item, delivered, err)
	}
	ready, err := s.StartDelivered(ctx, "resend")
	if err != nil || ready {
		t.Fatalf("start delivered=%v err=%v", ready, err)
	}
	if err := s.Delivered(ctx, "start-"+item.TaskID); err != nil {
		t.Fatal(err)
	}
	ready, err = s.StartDelivered(ctx, "resend")
	if err != nil || !ready {
		t.Fatalf("start delivered=%v err=%v", ready, err)
	}
}
