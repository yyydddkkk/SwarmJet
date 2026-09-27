package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"swarmjet/internal/domain"
	"swarmjet/internal/mq"
	"swarmjet/internal/pipeline"
	"swarmjet/internal/store"
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
func (s *Service) ConsumeCommand(ctx context.Context, body []byte) error {
	var message commandMessage
	if err := json.Unmarshal(body, &message); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidCommandDelivery, err)
	}
	if message.ID == "" || message.TaskID == "" {
		return ErrInvalidCommandDelivery
	}
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
		name, queue := workflowRoute(t)
		_, err = s.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: t.ID, TaskQueue: queue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, name, t)
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
