// Package mq owns the durable RabbitMQ queues used between the API and workers.
package mq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	WebhookQueue = "swarmjet.webhooks"
	CommandQueue = "swarmjet.commands"
	deadExchange = "swarmjet.dead"
)

var ErrUnroutable = errors.New("RabbitMQ message was not routed")
var ErrRejected = errors.New("RabbitMQ rejected the publication")

type dispositionError struct {
	err       error
	permanent bool
}

func (e dispositionError) Error() string { return e.err.Error() }
func (e dispositionError) Unwrap() error { return e.err }

// Defer retries a temporary condition, such as admission capacity, without
// consuming the quorum queue's poison-message delivery limit.
func Defer(err error) error { return dispositionError{err: err} }

// Permanent sends an invalid message straight to its dead-letter queue.
func Permanent(err error) error { return dispositionError{err: err, permanent: true} }

type Broker struct {
	url   string
	mu    sync.Mutex
	conn  *amqp.Connection
	slots chan struct{}
}

func New(url string) *Broker { return &Broker{url: url, slots: make(chan struct{}, 32)} }

// Ping checks that RabbitMQ can accept a connection and declare our queues.
func (b *Broker) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, err := amqp.DialConfig(b.url, amqp.Config{Dial: func(network, addr string) (net.Conn, error) {
		socket, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = socket.SetDeadline(deadline)
		}
		return socket, nil
	}, Heartbeat: 10 * time.Second})
	if err != nil {
		return err
	}
	defer conn.Close()
	return declare(conn)
}

func traceHeaders(ctx context.Context) amqp.Table {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	headers := amqp.Table{}
	for key, value := range carrier {
		headers[key] = value
	}
	return headers
}

func deliveryContext(ctx context.Context, headers amqp.Table) context.Context {
	carrier := propagation.MapCarrier{}
	for key, value := range headers {
		if s, ok := value.(string); ok && (key == "traceparent" || key == "tracestate") {
			carrier[key] = s
		}
	}
	return otel.GetTextMapPropagator().Extract(ctx, carrier)
}

func (b *Broker) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		return nil
	}
	err := b.conn.Close()
	b.conn = nil
	return err
}

func (b *Broker) connection() (*amqp.Connection, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil && !b.conn.IsClosed() {
		return b.conn, nil
	}
	conn, err := amqp.DialConfig(b.url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second), Heartbeat: 10 * time.Second})
	if err != nil {
		return nil, err
	}
	if err = declare(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	b.conn = conn
	return conn, nil
}

func queueArgs(queue string) amqp.Table {
	return amqp.Table{
		"x-queue-type":              "quorum",
		"x-overflow":                "reject-publish",
		"x-max-length":              int32(100000),
		"x-max-length-bytes":        int64(512 << 20),
		"x-delivery-limit":          int32(20),
		"x-delayed-retry-type":      "all",
		"x-delayed-retry-min":       int32(1000),
		"x-delayed-retry-max":       int32(30000),
		"x-dead-letter-strategy":    "at-least-once",
		"x-dead-letter-exchange":    deadExchange,
		"x-dead-letter-routing-key": queue + ".dead",
	}
}

func declare(conn *amqp.Connection) error {
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err = ch.ExchangeDeclare(deadExchange, "direct", true, false, false, false, nil); err != nil {
		return err
	}
	for _, name := range []string{WebhookQueue, CommandQueue} {
		dead := name + ".dead"
		if _, err = ch.QueueDeclare(dead, true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); err != nil {
			return err
		}
		if err = ch.QueueBind(dead, dead, deadExchange, false, nil); err != nil {
			return err
		}
		if _, err = ch.QueueDeclare(name, true, false, false, false, queueArgs(name)); err != nil {
			return err
		}
	}
	return nil
}

// Publish succeeds only after a positive publisher confirm and a route to the
// requested queue. A channel per publication keeps basic.return correlated with
// its confirm even when many HTTP requests publish concurrently.
func (b *Broker) Publish(ctx context.Context, queue string, body []byte, id string) error {
	if queue != WebhookQueue && queue != CommandQueue {
		return fmt.Errorf("unknown queue %q", queue)
	}
	ctx, span := otel.Tracer("swarmjet/rabbitmq").Start(ctx, "rabbitmq.publish", trace.WithSpanKind(trace.SpanKindProducer), trace.WithAttributes(attribute.String("messaging.destination.name", queue)))
	defer span.End()
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	conn, err := b.connection()
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err = ch.Confirm(false); err != nil {
		return err
	}
	returns := ch.NotifyReturn(make(chan amqp.Return, 1))
	confirmation, err := ch.PublishWithDeferredConfirmWithContext(ctx, "", queue, true, false, amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		ContentType:  "application/json",
		MessageId:    id,
		Headers:      traceHeaders(ctx),
		Body:         body,
	})
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-confirmation.Done():
		if !confirmation.Acked() {
			return ErrRejected
		}
		select {
		case <-returns:
			return ErrUnroutable
		default:
			return nil
		}
	}
}

// Consume reconnects after broker or channel failure. The handler must finish
// its durable side effects before returning nil, which triggers manual ACK.
func (b *Broker) Consume(ctx context.Context, queue string, handler func(context.Context, []byte) error) {
	for ctx.Err() == nil {
		if err := b.consumeOnce(ctx, queue, handler); err != nil && ctx.Err() == nil {
			slog.Error("RabbitMQ consumer disconnected", "queue", queue, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// ConsumeBatch collects up to 32 prefetched deliveries over a short window.
// Each result is settled independently, so a failed repository scan does not
// hold unrelated deliveries in the same batch.
func (b *Broker) ConsumeBatch(ctx context.Context, queue string, handler func(context.Context, [][]byte) []error) {
	for ctx.Err() == nil {
		if err := b.consumeBatchOnce(ctx, queue, handler); err != nil && ctx.Err() == nil {
			slog.Error("RabbitMQ batch consumer disconnected", "queue", queue, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (b *Broker) consumeBatchOnce(ctx context.Context, queue string, handler func(context.Context, [][]byte) []error) error {
	if queue != WebhookQueue && queue != CommandQueue {
		return fmt.Errorf("unknown queue %q", queue)
	}
	conn, err := amqp.DialConfig(b.url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second), Heartbeat: 10 * time.Second})
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = declare(conn); err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err = ch.Qos(32, 0, false); err != nil {
		return err
	}
	deliveries, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	for {
		var first amqp.Delivery
		var ok bool
		select {
		case <-ctx.Done():
			return nil
		case first, ok = <-deliveries:
			if !ok {
				return errors.New("delivery channel closed")
			}
		}
		batch := []amqp.Delivery{first}
		window := time.NewTimer(20 * time.Millisecond)
	collect:
		for len(batch) < 32 {
			select {
			case <-ctx.Done():
				window.Stop()
				return nil
			case delivery, open := <-deliveries:
				if !open {
					window.Stop()
					return errors.New("delivery channel closed")
				}
				batch = append(batch, delivery)
			case <-window.C:
				break collect
			}
		}
		window.Stop()
		bodies := make([][]byte, len(batch))
		links := make([]trace.Link, 0, len(batch))
		for i := range batch {
			bodies[i] = batch[i].Body
			if sc := trace.SpanContextFromContext(deliveryContext(ctx, batch[i].Headers)); sc.IsValid() {
				links = append(links, trace.Link{SpanContext: sc})
			}
		}
		// A single delivery continues its producer trace. For a coalesced batch,
		// the first delivery is the parent and the remaining producers are links.
		parentCtx := deliveryContext(ctx, first.Headers)
		batchCtx, span := otel.Tracer("swarmjet/rabbitmq").Start(parentCtx, "rabbitmq.consume_batch", trace.WithSpanKind(trace.SpanKindConsumer), trace.WithLinks(links...), trace.WithAttributes(attribute.String("messaging.destination.name", queue)))
		results := handler(batchCtx, bodies)
		span.End()
		if len(results) != len(batch) {
			return fmt.Errorf("batch handler returned %d results for %d deliveries", len(results), len(batch))
		}
		for i, result := range results {
			if err := settle(queue, batch[i], result); err != nil {
				return err
			}
		}
	}
}

func settle(queue string, d amqp.Delivery, result error) error {
	if result == nil {
		return d.Ack(false)
	}
	var disposition dispositionError
	switch {
	case errors.As(result, &disposition) && disposition.permanent:
		slog.Error("RabbitMQ message dead-lettered", "queue", queue, "message_id", d.MessageId, "error", result)
		return d.Reject(false)
	case errors.As(result, &disposition):
		return d.Nack(false, true)
	default:
		slog.Warn("RabbitMQ message retrying", "queue", queue, "message_id", d.MessageId, "error", result)
		return d.Reject(true)
	}
}

func (b *Broker) consumeOnce(ctx context.Context, queue string, handler func(context.Context, []byte) error) error {
	if queue != WebhookQueue && queue != CommandQueue {
		return fmt.Errorf("unknown queue %q", queue)
	}
	conn, err := amqp.DialConfig(b.url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second), Heartbeat: 10 * time.Second})
	if err != nil {
		return err
	}
	defer conn.Close()
	if err = declare(conn); err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err = ch.Qos(32, 0, false); err != nil {
		return err
	}
	deliveries, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("delivery channel closed")
			}
			messageCtx, span := otel.Tracer("swarmjet/rabbitmq").Start(deliveryContext(ctx, d.Headers), "rabbitmq.consume", trace.WithSpanKind(trace.SpanKindConsumer), trace.WithAttributes(attribute.String("messaging.destination.name", queue)))
			result := handler(messageCtx, d.Body)
			span.End()
			err := settle(queue, d, result)
			if err != nil {
				return err
			}
		}
	}
}
