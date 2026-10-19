// Package jobs is the process boundary around River. Business code inserts and
// handles jobs through these types and never imports River itself.
package jobs

import (
	"context"
	"errors"
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
	messageQueue = "messages"
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

// Message is one Inbox delivery. The delivery worker only reports the message
// as delivered; pushing it into a CLI session is the next round's Cell runtime.
type Message struct {
	ID string
}

// Inserter adds a job to an existing transaction. InsertMessage is deliberately
// not part of this interface so existing implementations keep compiling; a
// caller that needs it asserts for the method instead.
type Inserter interface {
	InsertWebhook(context.Context, pgx.Tx, Webhook) error
}

// Handlers run jobs. A nil handler is a permanent failure.
type Handlers struct {
	Webhook func(context.Context, Webhook) error
	Message func(context.Context, Message) error
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

type messageArgs struct {
	ID string `json:"id" river:"unique"`
}

func (messageArgs) Kind() string { return "message" }

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
	river.AddWorker(workers, &messageWorker{handle: handlers.Message})
	config := &river.Config{
		Logger: slog.New(slog.DiscardHandler),
		Queues: map[string]river.QueueConfig{
			webhookQueue: {MaxWorkers: 8},
			messageQueue: {MaxWorkers: 8},
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

// InsertMessage enqueues one Inbox delivery in the caller's transaction.
func (c *Client) InsertMessage(ctx context.Context, tx pgx.Tx, job Message) error {
	_, err := c.river.InsertTx(ctx, tx, messageArgs{ID: job.ID}, &river.InsertOpts{
		Queue:      messageQueue,
		UniqueOpts: river.UniqueOpts{ByArgs: true},
	})
	return err
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

type messageWorker struct {
	river.WorkerDefaults[messageArgs]
	handle func(context.Context, Message) error
}

func (w *messageWorker) Work(ctx context.Context, job *river.Job[messageArgs]) error {
	if w.handle == nil {
		return river.JobCancel(errors.New("message handler is not configured"))
	}
	return disposition(w.handle(ctx, Message{ID: job.Args.ID}))
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
