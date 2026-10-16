package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.temporal.io/sdk/client"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
)

type commandTemporal struct {
	client.Client
	starts      int
	signals     int
	signalError error
}

func (t *commandTemporal) ExecuteWorkflow(_ context.Context, options client.StartWorkflowOptions, _ interface{}, _ ...interface{}) (client.WorkflowRun, error) {
	if options.ID != "command-test" {
		return nil, errors.New("unexpected workflow ID")
	}
	t.starts++
	return nil, nil
}

func (t *commandTemporal) SignalWorkflow(_ context.Context, workflowID, _, _ string, _ interface{}) error {
	if workflowID != "command-test" {
		return errors.New("unexpected signal workflow ID")
	}
	t.signals++
	return t.signalError
}

func TestConsumeCommandMarksDeliveredOnlyAfterTemporal(t *testing.T) {
	s := scanTestStore(t)
	ctx := context.Background()
	project := domain.Project{ID: "command-project"}
	projectBody, _ := json.Marshal(project)
	if _, err := s.Pool.Exec(ctx, `INSERT INTO projects(id,version,body) VALUES($1,1,$2)`, project.ID, projectBody); err != nil {
		t.Fatal(err)
	}
	task := domain.Task{ID: "command-test", Project: project, Stage: "queued"}
	taskBody, _ := json.Marshal(task)
	if _, err := s.Pool.Exec(ctx, `INSERT INTO tasks(id,project_id,source,stage,body) VALUES($1,$2,$3,$4,$5)`, task.ID, project.ID, "command-test", task.Stage, taskBody); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, task.ID, domain.Command{ID: "start-command-test", Kind: "start"}); err != nil {
		t.Fatal(err)
	}
	temporal := &commandTemporal{}
	svc := &Service{Store: s, Temporal: temporal}
	note := domain.Command{ID: "note-command-test", Kind: "note", Text: "once"}
	if err := s.Enqueue(ctx, task.ID, note); err != nil {
		t.Fatal(err)
	}
	early, _ := json.Marshal(commandMessage{ID: note.ID, TaskID: task.ID})
	if err := svc.ConsumeCommand(ctx, early); !errors.Is(err, jobs.ErrSnooze) || temporal.signals != 0 {
		t.Fatalf("note delivered before start: signals=%d err=%v", temporal.signals, err)
	}
	start, _ := json.Marshal(commandMessage{ID: "start-command-test", TaskID: task.ID})
	if err := svc.ConsumeCommand(ctx, start); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConsumeCommand(ctx, start); err != nil || temporal.starts != 1 {
		t.Fatalf("duplicate start executed: starts=%d err=%v", temporal.starts, err)
	}
	message, _ := json.Marshal(commandMessage{ID: note.ID, TaskID: task.ID})
	temporal.signalError = errors.New("Temporal unavailable")
	if err := svc.ConsumeCommand(ctx, message); err == nil {
		t.Fatal("failed Temporal signal was acknowledged")
	}
	_, delivered, err := s.CommandForDelivery(ctx, note.ID)
	if err != nil || delivered {
		t.Fatalf("failed signal marked delivered: %v %v", delivered, err)
	}
	temporal.signalError = nil
	if err := svc.ConsumeCommand(ctx, message); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConsumeCommand(ctx, message); err != nil || temporal.signals != 2 {
		t.Fatalf("duplicate signal executed: signals=%d err=%v", temporal.signals, err)
	}
}
