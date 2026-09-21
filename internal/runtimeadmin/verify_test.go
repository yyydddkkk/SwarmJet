package runtimeadmin

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func callVerify(t *testing.T, s *server, model string) *httptest.ResponseRecorder {
	t.Helper()
	body := bytes.NewReader([]byte("{\"model\":\"" + model + "\"}"))
	req := httptest.NewRequest("POST", "/verify", body)
	req.Header.Set("X-SwarmJet-Runtime-Secret", "worker-secret")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestVerifyModelRejectsInvalidIdentifier(t *testing.T) {
	s, _ := setup(t)
	rec := callVerify(t, s, "not a model")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "provider/model") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestVerifyModelSucceedsWithoutLeakingSecrets(t *testing.T) {
	s, _ := setup(t)
	rec := callVerify(t, s, "openai/gpt-test")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "\"ok\":true") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	for _, secret := range []string{"provider-secret", "worker-secret", "must-not-pass"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("response leaked %s: %s", secret, rec.Body)
		}
	}
}

func TestVerifyModelReportsCallFailure(t *testing.T) {
	s, f := setup(t)
	original := f.run
	s.cfg.DockerCommand = func(ctx context.Context, args, env []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "exec" {
			return nil, errors.New("provider rejected the request")
		}
		return original(ctx, args, env)
	}
	rec := callVerify(t, s, "openai/gpt-test")
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "模型调用失败") {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}

func TestVerifyModelTimesOut(t *testing.T) {
	s, f := setup(t)
	original := f.run
	s.cfg.DockerCommand = func(ctx context.Context, args, env []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "exec" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return original(ctx, args, env)
	}
	body := bytes.NewReader([]byte("{\"model\":\"openai/gpt-test\"}"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("POST", "/verify", body).WithContext(ctx)
	req.Header.Set("X-SwarmJet-Runtime-Secret", "worker-secret")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
}
