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
	"github.com/yyydddkkk/SwarmJet/internal/mq"
	"github.com/yyydddkkk/SwarmJet/internal/pipeline"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

const CommandQueue = mq.CommandQueue
const commandResendAfter = 5 * time.Minute

var ErrInvalidCommandDelivery = errors.New("invalid command queue message")

type CommandPublisher interface {
	Publish(context.Context, string, []byte, string) error
}

type commandMessage struct {
	ID     string `json:"id"`
	TaskID string `json:"taskId"`
}

// PublishCommands leaves the database intent pending after every publish
// failure, including an ambiguous publisher-confirm timeout.
func (s *Service) PublishCommands(ctx context.Context, publisher CommandPublisher) error {
	if publisher == nil {
		return errors.New("command publisher is required")
	}
	commands, err := s.Store.CommandsForPublication(ctx, commandResendAfter)
	if err != nil {
		return err
	}
	for _, item := range commands {
		message, err := json.Marshal(commandMessage{ID: item.Command.ID, TaskID: item.TaskID})
		if err == nil {
			err = publisher.Publish(ctx, CommandQueue, message, item.Command.ID)
		}
		if err == nil {
			err = s.Store.MarkCommandPublished(ctx, item.Command.ID)
		}
		if err != nil {
			return fmt.Errorf("publish command %s: %w", item.Command.ID, err)
		}
	}
	return nil
}

// ConsumeCommand applies a durable command before its RabbitMQ delivery is
// acknowledged. Repeated start messages use the fixed Workflow ID; repeated
// signals are ignored inside each Workflow by command ID.
func (s *Service) ConsumeCommand(ctx context.Context, body []byte) (err error) {
	start := time.Now()
	var message commandMessage
	if decodeErr := json.Unmarshal(body, &message); decodeErr != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCommandDelivery, decodeErr)
	}
	if message.ID == "" || message.TaskID == "" {
		return ErrInvalidCommandDelivery
	}
	defer func() {
		result := "ok"
		if err != nil {
			result = "error"
		}
		slog.Info("task command processed", "task_id", message.TaskID, "command_id", message.ID, "result", result, "duration_ms", time.Since(start).Milliseconds())
	}()
	item, delivered, err := s.Store.CommandForDelivery(ctx, message.ID)
	if err != nil {
		return err
	}
	if item.TaskID != message.TaskID || item.Command.ID != message.ID {
		return ErrInvalidCommandDelivery
	}
	if delivered {
		return nil
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
