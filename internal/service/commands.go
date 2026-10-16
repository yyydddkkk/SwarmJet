package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
	"github.com/yyydddkkk/SwarmJet/internal/pipeline"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

var ErrInvalidCommandDelivery = errors.New("invalid command queue message")

type commandMessage struct {
	ID     string `json:"id"`
	TaskID string `json:"taskId"`
}

// HandleCommandJob delivers one command to Temporal and then marks it delivered.
// A command other than start waits until the fixed start command has been delivered.
// Repeating a delivered command does not call Temporal again.
func (s *Service) HandleCommandJob(ctx context.Context, job jobs.Command) error {
	body, err := json.Marshal(commandMessage{ID: job.ID, TaskID: job.TaskID})
	if err != nil {
		return fmt.Errorf("%w: %w", jobs.ErrCancel, err)
	}
	return s.ConsumeCommand(ctx, body)
}

// ConsumeCommand applies a durable command. Repeated start messages use the
// fixed Workflow ID; repeated signals are ignored inside each Workflow by command ID.
func (s *Service) ConsumeCommand(ctx context.Context, body []byte) (err error) {
	start := time.Now()
	var message commandMessage
	if decodeErr := json.Unmarshal(body, &message); decodeErr != nil {
		return fmt.Errorf("%w: %w", jobs.ErrCancel, fmt.Errorf("%w: %v", ErrInvalidCommandDelivery, decodeErr))
	}
	if message.ID == "" || message.TaskID == "" {
		return fmt.Errorf("%w: %w", jobs.ErrCancel, ErrInvalidCommandDelivery)
	}
	defer func() {
		result := "ok"
		if err != nil && !errors.Is(err, jobs.ErrSnooze) {
			result = "error"
		}
		slog.Info("task command processed", "task_id", message.TaskID, "command_id", message.ID, "result", result, "duration_ms", time.Since(start).Milliseconds())
	}()
	item, delivered, err := s.Store.CommandForDelivery(ctx, message.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: %w", jobs.ErrCancel, err)
		}
		return err
	}
	if item.TaskID != message.TaskID || item.Command.ID != message.ID {
		return fmt.Errorf("%w: %w", jobs.ErrCancel, ErrInvalidCommandDelivery)
	}
	if delivered {
		return nil
	}
	if item.Command.Kind != "start" {
		ready, err := s.Store.StartDelivered(ctx, item.TaskID)
		if err != nil {
			return err
		}
		if !ready {
			return fmt.Errorf("%w: start command is not delivered", jobs.ErrSnooze)
		}
	}
	if err := s.deliverCommand(ctx, item); err != nil {
		return err
	}
	return s.Store.Delivered(ctx, message.ID)
}

func (s *Service) deliverCommand(ctx context.Context, item store.PendingCommand) error {
	if item.Command.Kind == "start" {
		t, err := s.Store.Task(ctx, item.TaskID)
		if err != nil {
			return err
		}
		_, err = s.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: t.ID, TaskQueue: pipeline.Queue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, pipeline.WorkflowName, t)
		var started *serviceerror.WorkflowExecutionAlreadyStarted
		if err != nil && !errors.As(err, &started) {
			return err
		}
		return nil
	}
	err := s.Temporal.SignalWorkflow(ctx, item.TaskID, "", pipeline.SignalName, item.Command)
	if err != nil {
		var gone *serviceerror.NotFound
		if errors.As(err, &gone) {
			t, readErr := s.Store.Task(ctx, item.TaskID)
			if readErr == nil && domain.Terminal(t.Stage) {
				return nil
			}
		}
	}
	return err
}
