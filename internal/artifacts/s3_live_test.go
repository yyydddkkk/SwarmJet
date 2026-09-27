package artifacts

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Run with a disposable S3-compatible endpoint. It is skipped in ordinary CI.
func TestS3LiveConditionalWriteAndCleanup(t *testing.T) {
	endpoint := os.Getenv("SWARMJET_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("set SWARMJET_TEST_S3_ENDPOINT for isolated object-store validation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg := S3Config{
		Bucket: "swarmjet-validation-" + time.Now().UTC().Format("20060102150405"),
		Region: "us-east-1", Prefix: "test", Endpoint: endpoint, PathStyle: true,
		AccessKeyID:     os.Getenv("SWARMJET_TEST_S3_ACCESS_KEY"),
		SecretAccessKey: os.Getenv("SWARMJET_TEST_S3_SECRET_KEY"),
	}
	store, err := NewS3Store(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	client := store.client.(*s3.Client)
	if _, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = client.DeleteBucket(context.Background(), &s3.DeleteBucketInput{Bucket: aws.String(cfg.Bucket)})
	}()
	r := Request{TaskID: "task", ExecutionID: "exec", Kind: Result, Name: "result", Role: "dev"}
	body := `{"outcome":"changed","summary":"live"}`
	ref, err := store.Put(ctx, r, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Put(ctx, r, strings.NewReader(body)); err != nil {
		t.Fatal("idempotent put", err)
	}
	if _, err = store.Put(ctx, r, strings.NewReader(`{"outcome":"changed","summary":"different"}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("overwrite accepted: %v", err)
	}
	if got, err := store.Read(ctx, ref); err != nil || string(got) != body {
		t.Fatalf("read %q %v", got, err)
	}
	if _, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(store.key(r)), Body: strings.NewReader("tampered")}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Read(ctx, ref); err == nil {
		t.Fatal("tampered object accepted")
	}
	if err = store.DeleteTask(ctx, "task"); err != nil {
		t.Fatal(err)
	}
	if err = store.DeleteTask(ctx, "task"); err != nil {
		t.Fatal("idempotent cleanup", err)
	}
	if _, err = store.Read(ctx, ref); err == nil {
		t.Fatal("deleted object still readable")
	}
}
