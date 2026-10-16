// Package jobs is the process boundary around River. Business code inserts and
// handles jobs through these types and never imports River itself.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

const (
	webhookQueue = "webhooks"
	commandQueue = "commands"
)

// ErrSnooze asks the queue to run the job again later without consuming an attempt.
var ErrSnooze = errors.New("job waiting")

// ErrCancel asks the queue to stop retrying a job that can never succeed.
var ErrCancel = errors.New("job cancelled")

// Webhook is one accepted provider delivery.
type Webhook struct {
	Provider   string
	DeliveryID string
	EventType  string
	Payload    []byte
}

// Command is one durable task command.
type Command struct {
	ID     string
	TaskID string
}

// Inserter adds a job to an existing transaction.
type Inserter interface {
	InsertWebhook(context.Context, pgx.Tx, Webhook) error
	InsertCommand(context.Context, pgx.Tx, Command) error
}

// Handlers run jobs. A nil handler is a permanent failure.
type Handlers struct {
	Webhook func(context.Context, Webhook) error
	Command func(context.Context, Command) error
}

// Config tunes the worker. Zero values use River's defaults.
type Config struct {
	PollInterval time.Duration
}

type webhookArgs struct {
	Provider   string `json:"provider" river:"unique"`
	DeliveryID string `json:"deliveryId" river:"unique"`
	EventType  string `json:"eventType"`
	Payload    []byte `json:"payload"`
}

func (webhookArgs) Kind() string { return "webhook" }

type commandArgs struct {
	ID     string `json:"id" river:"unique"`
	TaskID string `json:"taskId" river:"unique"`
}

func (commandArgs) Kind() string { return "command" }

type Client struct {
	river *river.Client[pgx.Tx]
}

// Migrate applies River's schema migrations. Repeating the call is safe.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	return err
}

// Open prepares a client that can insert jobs and, after Start, work them.
func Open(pool *pgxpool.Pool, handlers Handlers, cfg Config) (*Client, error) {
	workers := river.NewWorkers()
	river.AddWorker(workers, &webhookWorker{handle: handlers.Webhook})
	river.AddWorker(workers, &commandWorker{handle: handlers.Command})
	config := &river.Config{
		Logger: slog.New(slog.DiscardHandler),
		Queues: map[string]river.QueueConfig{
			webhookQueue: {MaxWorkers: 8},
			commandQueue: {MaxWorkers: 8},
		},
		Workers:    workers,
		JobTimeout: 10 * time.Minute,
	}
	if cfg.PollInterval > 0 {
		config.FetchCooldown = cfg.PollInterval
		config.FetchPollInterval = cfg.PollInterval
	}
	client, err := river.NewClient(riverpgxv5.New(pool), config)
	if err != nil {
		return nil, err
	}
	return &Client{river: client}, nil
}

// Start works jobs until ctx is cancelled.
func (c *Client) Start(ctx context.Context) error {
	return c.river.Start(ctx)
}

func (c *Client) InsertWebhook(ctx context.Context, tx pgx.Tx, job Webhook) error {
	_, err := c.river.InsertTx(ctx, tx, webhookArgs{Provider: job.Provider, DeliveryID: job.DeliveryID, EventType: job.EventType, Payload: job.Payload}, &river.InsertOpts{
		Queue:      webhookQueue,
		UniqueOpts: river.UniqueOpts{ByArgs: true},
	})
	return err
}

func (c *Client) InsertCommand(ctx context.Context, tx pgx.Tx, job Command) error {
	_, err := c.river.InsertTx(ctx, tx, commandArgs{ID: job.ID, TaskID: job.TaskID}, &river.InsertOpts{
		Queue:      commandQueue,
		UniqueOpts: river.UniqueOpts{ByArgs: true},
	})
	return err
}

// RecoverCommands inserts jobs for commands that were committed before a queue
// client existed. Unique command IDs keep a second call from delivering twice.
func (c *Client) RecoverCommands(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `SELECT id, task_id FROM commands WHERE NOT delivered ORDER BY created_at, id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type pending struct{ id, taskID string }
	var commands []pending
	for rows.Next() {
		var item pending
		if err = rows.Scan(&item.id, &item.taskID); err != nil {
			return err
		}
		commands = append(commands, item)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(commands) == 0 {
		return nil
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, item := range commands {
		if err = c.InsertCommand(ctx, tx, Command{ID: item.id, TaskID: item.taskID}); err != nil {
			return fmt.Errorf("queue command %s: %w", item.id, err)
		}
	}
	return tx.Commit(ctx)
}

type webhookWorker struct {
	river.WorkerDefaults[webhookArgs]
	handle func(context.Context, Webhook) error
}

func (w *webhookWorker) Work(ctx context.Context, job *river.Job[webhookArgs]) error {
	if w.handle == nil {
		return river.JobCancel(errors.New("webhook handler is not configured"))
	}
	return disposition(w.handle(ctx, Webhook{Provider: job.Args.Provider, DeliveryID: job.Args.DeliveryID, EventType: job.Args.EventType, Payload: job.Args.Payload}))
}

type commandWorker struct {
	river.WorkerDefaults[commandArgs]
	handle func(context.Context, Command) error
}

func (w *commandWorker) Work(ctx context.Context, job *river.Job[commandArgs]) error {
	if w.handle == nil {
		return river.JobCancel(errors.New("command handler is not configured"))
	}
	return disposition(w.handle(ctx, Command{ID: job.Args.ID, TaskID: job.Args.TaskID}))
}

func disposition(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrCancel):
		return river.JobCancel(err)
	case errors.Is(err, ErrSnooze):
		return river.JobSnooze(2 * time.Second)
	default:
		return err
	}
}
