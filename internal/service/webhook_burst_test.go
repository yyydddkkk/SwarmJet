package service

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"swarmjet/internal/mq"
)

// This is an opt-in load acceptance test. Its PostgreSQL schema is isolated and
// the RabbitMQ instance must be disposable because the test purges its queues.
func TestWebhookBurstIntegration(t *testing.T) {
	if os.Getenv("SWARMJET_BURST_TEST") != "1" {
		t.Skip("set SWARMJET_BURST_TEST=1 for the 10,000-delivery test")
	}
	url := os.Getenv("SWARMJET_RABBITMQ_TEST_URL")
	if url == "" {
		t.Fatal("SWARMJET_RABBITMQ_TEST_URL is required")
	}
	store := scanTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	broker := mq.New(url)
	defer broker.Close()
	if err := broker.Publish(ctx, mq.WebhookQueue, []byte("warmup"), "warmup"); err != nil {
		t.Fatal(err)
	}
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	if _, err := ch.QueuePurge(mq.WebhookQueue, false); err != nil {
		t.Fatal(err)
	}
	svc := &Service{Store: store, WebhookPublisher: broker}
	var deliveries atomic.Int64
	go broker.ConsumeBatch(ctx, mq.WebhookQueue, func(ctx context.Context, bodies [][]byte) []error {
		deliveries.Add(int64(len(bodies)))
		return svc.ProcessWebhookDeliveries(ctx, bodies)
	})
	server := httptest.NewServer(svc.Webhook("burst-secret"))
	defer server.Close()
	const total = 10000
	jobs := make(chan int, total)
	for i := 0; i < total; i++ {
		jobs <- i
	}
	close(jobs)
	var firstAccepted, retries, accepted atomic.Int64
	latencies := make([]time.Duration, total)
	var wg sync.WaitGroup
	start := time.Now()
	client := &http.Client{Timeout: 7 * time.Second}
	for worker := 0; worker < 16; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				for attempt := 0; ; attempt++ {
					request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewBufferString(`{"project":{"id":7}}`))
					if err != nil {
						t.Error(err)
						return
					}
					request.Header.Set("X-Gitlab-Token", "burst-secret")
					request.Header.Set("webhook-id", fmt.Sprintf("burst-%d", id))
					began := time.Now()
					response, err := client.Do(request)
					if err == nil {
						response.Body.Close()
					}
					if err == nil && response.StatusCode == http.StatusAccepted {
						latencies[id] = time.Since(began)
						accepted.Add(1)
						if attempt == 0 {
							firstAccepted.Add(1)
						}
						break
					}
					if err == nil && response.StatusCode != http.StatusServiceUnavailable {
						t.Errorf("delivery %d status %d", id, response.StatusCode)
						return
					}
					retries.Add(1)
					if ctx.Err() != nil {
						t.Errorf("delivery %d never accepted: %v", id, ctx.Err())
						return
					}
					time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
	ackTime := time.Since(start)
	if accepted.Load() != total {
		t.Fatalf("final accepted=%d want=%d", accepted.Load(), total)
	}
	var processed int
	var lockWaits int
	for {
		if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM webhook_receipts WHERE provider='gitlab'").Scan(&processed); err != nil {
			t.Fatal(err)
		}
		var waiting int
		if err := store.Pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%webhook_receipts%'").Scan(&waiting); err == nil {
			lockWaits += waiting
		}
		if processed == total {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("processed %d/%d before timeout", processed, total)
		}
		time.Sleep(100 * time.Millisecond)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	percentile := func(p float64) time.Duration { return latencies[int(float64(total-1)*p)] }
	t.Logf("10k webhook burst: first ACK=%d final ACK=%d 503 retries=%d p50=%s p95=%s p99=%s ACK phase=%s drain=%s received deliveries=%d unique processed=%d lost=%d duplicate deliveries=%d sampled PG lock waits=%d", firstAccepted.Load(), accepted.Load(), retries.Load(), percentile(.5), percentile(.95), percentile(.99), ackTime, time.Since(start), deliveries.Load(), processed, total-processed, deliveries.Load()-total, lockWaits)
}

func TestWebhookAPIAndConsumerRestartIntegration(t *testing.T) {
	url := os.Getenv("SWARMJET_RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("set SWARMJET_RABBITMQ_TEST_URL to a disposable Broker")
	}
	s := scanTestStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	broker := mq.New(url)
	defer broker.Close()
	if err := broker.Publish(ctx, mq.WebhookQueue, []byte("warmup"), "warmup"); err != nil {
		t.Fatal(err)
	}
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ch.QueuePurge(mq.WebhookQueue, false); err != nil {
		t.Fatal(err)
	}
	_ = ch.Close()
	_ = conn.Close()
	send := func(first, last int) {
		server := httptest.NewServer((&Service{Store: s, WebhookPublisher: broker}).Webhook("restart-secret"))
		defer server.Close()
		for i := first; i < last; i++ {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewBufferString(`{"project":{"id":7}}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Gitlab-Token", "restart-secret")
			req.Header.Set("webhook-id", fmt.Sprintf("restart-%d", i))
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusAccepted {
				t.Fatalf("delivery %d status %d", i, response.StatusCode)
			}
		}
	}
	for phase := 0; phase < 2; phase++ {
		send(phase*25, (phase+1)*25)
		consumerCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			broker.ConsumeBatch(consumerCtx, mq.WebhookQueue, (&Service{Store: s}).ProcessWebhookDeliveries)
		}()
		for {
			var receipts int
			if err := s.Pool.QueryRow(ctx, "SELECT count(*) FROM webhook_receipts").Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if receipts == (phase+1)*25 {
				break
			}
			if ctx.Err() != nil {
				t.Fatalf("phase %d receipts=%d: %v", phase, receipts, ctx.Err())
			}
			time.Sleep(25 * time.Millisecond)
		}
		stop()
		<-done
	}
}
