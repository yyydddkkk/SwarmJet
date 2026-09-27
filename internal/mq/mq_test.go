package mq

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Run only against a disposable RabbitMQ 4.3 instance: this test deletes and
// redeclares the webhook queue to exercise mandatory returns and overflow.
func TestBrokerIntegration(t *testing.T) {
	url := os.Getenv("SWARMJET_RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("set SWARMJET_RABBITMQ_TEST_URL to a disposable RabbitMQ 4.3 instance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	b := New(url)
	defer b.Close()
	conn, err := b.connection()
	if err != nil {
		t.Fatal(err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	for _, name := range []string{WebhookQueue, WebhookQueue + ".dead"} {
		if _, err := ch.QueuePurge(name, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Publish(ctx, WebhookQueue, []byte(`{"test":1}`), "confirmed"); err != nil {
		t.Fatal(err)
	}
	d, ok, err := ch.Get(WebhookQueue, false)
	if err != nil || !ok || d.MessageId != "confirmed" || d.DeliveryMode != amqp.Persistent {
		t.Fatalf("confirmed publication: %v %v %+v", err, ok, d)
	}
	if err := d.Reject(false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		dead, found, err := ch.Get(WebhookQueue+".dead", false)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			if dead.MessageId != "confirmed" {
				t.Fatalf("dead letter ID %q", dead.MessageId)
			}
			_ = dead.Ack(false)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dead letter did not arrive")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := ch.QueueDelete(WebhookQueue, false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(ctx, WebhookQueue, []byte("unroutable"), "unroutable"); !errors.Is(err, ErrUnroutable) {
		t.Fatalf("unroutable publication: %v", err)
	}
	args := queueArgs(WebhookQueue)
	args["x-max-length"] = int32(1)
	if _, err := ch.QueueDeclare(WebhookQueue, true, false, false, false, args); err != nil {
		t.Fatal(err)
	}
	rejected := false
	for i := 0; i < 20; i++ {
		err := b.Publish(ctx, WebhookQueue, []byte("full"), "full")
		if errors.Is(err, ErrRejected) {
			rejected = true
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !rejected {
		t.Fatal("overflow did not reject any publication")
	}
	if _, err := ch.QueueDelete(WebhookQueue, false, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.QueueDeclare(WebhookQueue, true, false, false, false, queueArgs(WebhookQueue)); err != nil {
		t.Fatal(err)
	}
	if err := b.Publish(ctx, WebhookQueue, []byte("poison"), "poison"); err != nil {
		t.Fatal(err)
	}
	consumeCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- b.consumeBatchOnce(consumeCtx, WebhookQueue, func(_ context.Context, bodies [][]byte) []error {
			stop()
			results := make([]error, len(bodies))
			for i := range results {
				results[i] = Permanent(errors.New("poison"))
			}
			return results
		})
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		dead, found, err := ch.Get(WebhookQueue+".dead", false)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			if dead.MessageId != "poison" {
				t.Fatalf("dead letter ID %q", dead.MessageId)
			}
			if err := b.Publish(ctx, WebhookQueue, dead.Body, dead.MessageId); err != nil {
				t.Fatal(err)
			}
			if err := dead.Ack(false); err != nil {
				t.Fatal(err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("consumer dead letter did not arrive")
		}
		time.Sleep(50 * time.Millisecond)
	}
	replayed, found, err := ch.Get(WebhookQueue, false)
	if err != nil || !found || replayed.MessageId != "poison" {
		t.Fatalf("replay: %v %v %+v", err, found, replayed)
	}
	if err := replayed.Ack(false); err != nil {
		t.Fatal(err)
	}
}

// Run publish, restart the disposable Broker container, then run verify.
func TestBrokerRestartPersistence(t *testing.T) {
	phase := os.Getenv("SWARMJET_RABBITMQ_RESTART_PHASE")
	if phase == "" {
		t.Skip("set SWARMJET_RABBITMQ_RESTART_PHASE=publish or verify")
	}
	url := os.Getenv("SWARMJET_RABBITMQ_TEST_URL")
	if url == "" {
		t.Fatal("SWARMJET_RABBITMQ_TEST_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b := New(url)
	defer b.Close()
	conn, err := b.connection()
	if err != nil {
		t.Fatal(err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	switch phase {
	case "publish":
		if _, err := ch.QueuePurge(CommandQueue, false); err != nil {
			t.Fatal(err)
		}
		if err := b.Publish(ctx, CommandQueue, []byte("restart-survivor"), "restart-survivor"); err != nil {
			t.Fatal(err)
		}
	case "verify":
		d, ok, err := ch.Get(CommandQueue, false)
		if err != nil || !ok || d.MessageId != "restart-survivor" || string(d.Body) != "restart-survivor" {
			t.Fatalf("restart recovery: %v %v %+v", err, ok, d)
		}
		if err := d.Ack(false); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown phase %q", phase)
	}
}
