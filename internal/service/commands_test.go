package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
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

type recordingCommandPublisher struct {
	messages [][]byte
	err      error
}

func (p *recordingCommandPublisher) Publish(_ context.Context, queue string, body []byte, id string) error {
	if queue != CommandQueue || id != "start-command-test" {
		return errors.New("unexpected command route")
	}
	if p.err != nil {
		return p.err
	}
	p.messages = append(p.messages, append([]byte(nil), body...))
	return nil
}

func TestPublishCommandsConfirmsAndResends(t *testing.T) {
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
	command := domain.Command{ID: "start-command-test", Kind: "start"}
	if err := s.Enqueue(ctx, task.ID, command); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: s}
	publisher := &recordingCommandPublisher{err: errors.New("confirm timed out")}
	if err := svc.PublishCommands(ctx, publisher); err == nil {
		t.Fatal("publisher failure was lost")
	}
	var publishedAt *time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT published_at FROM commands WHERE id=$1`, command.ID).Scan(&publishedAt); err != nil || publishedAt != nil {
		t.Fatalf("unconfirmed command marked published: %v %v", publishedAt, err)
	}
	publisher.err = nil
	if err := svc.PublishCommands(ctx, publisher); err != nil {
		t.Fatal(err)
	}
	if len(publisher.messages) != 1 {
		t.Fatalf("published %d times", len(publisher.messages))
	}
	var message commandMessage
	if err := json.Unmarshal(publisher.messages[0], &message); err != nil || message.ID != command.ID || message.TaskID != task.ID {
		t.Fatalf("message=%+v err=%v", message, err)
	}
	if err := svc.PublishCommands(ctx, publisher); err != nil || len(publisher.messages) != 1 {
		t.Fatalf("published again before resend deadline: %d %v", len(publisher.messages), err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE commands SET published_at=now()-interval '6 minutes' WHERE id=$1`, command.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.PublishCommands(ctx, publisher); err != nil || len(publisher.messages) != 2 {
		t.Fatalf("stale command not resent: %d %v", len(publisher.messages), err)
	}
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
	start, _ := json.Marshal(commandMessage{ID: "start-command-test", TaskID: task.ID})
	if err := svc.ConsumeCommand(ctx, start); err != nil {
		t.Fatal(err)
	}
	if err := svc.ConsumeCommand(ctx, start); err != nil || temporal.starts != 1 {
		t.Fatalf("duplicate start executed: starts=%d err=%v", temporal.starts, err)
	}
	note := domain.Command{ID: "note-command-test", Kind: "note", Text: "once"}
	if err := s.Enqueue(ctx, task.ID, note); err != nil {
		t.Fatal(err)
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
