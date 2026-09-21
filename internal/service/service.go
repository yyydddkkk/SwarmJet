package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"swarmjet/internal/domain"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/pipeline"
	"swarmjet/internal/store"
)

type Service struct {
	Store             *store.Store
	Temporal          client.Client
	ResolveCredential func(string) (string, error)
	Wake              chan struct{}
}

func ID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func (s *Service) Run(ctx context.Context) {
	fast := time.NewTicker(2 * time.Second)
	defer fast.Stop()
	slow := time.NewTicker(120 * time.Second)
	defer slow.Stop()
	if e := s.Scan(ctx); e != nil {
		slog.Error("scan", "error", e)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.Wake:
			if e := s.Scan(ctx); e != nil {
				slog.Error("webhook scan", "error", e)
			}
		case <-fast.C:
			if e := s.Deliver(ctx); e != nil {
				slog.Error("outbox delivery", "error", e)
			}
		case <-slow.C:
			if e := s.Scan(ctx); e != nil {
				slog.Error("scan", "error", e)
			}
		}
	}
}
func (s *Service) Deliver(ctx context.Context) error {
	pending, e := s.Store.Pending(ctx)
	if e != nil {
		return e
	}
	var lastErr error
	for _, item := range pending {
		if item.Command.Kind == "start" {
			t, e := s.Store.Task(ctx, item.TaskID)
			if e != nil {
				return e
			}
			_, e = s.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: t.ID, TaskQueue: pipeline.Queue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, pipeline.WorkflowName, t)
			var started *serviceerror.WorkflowExecutionAlreadyStarted
			if e != nil && !errors.As(e, &started) {
				lastErr = e
				continue
			}
		} else {
			e = s.Temporal.SignalWorkflow(ctx, item.TaskID, "", pipeline.SignalName, item.Command)
			if e != nil {
				var gone *serviceerror.NotFound
				t, readErr := s.Store.Task(ctx, item.TaskID)
				if errors.As(e, &gone) && readErr == nil && domain.Terminal(t.Stage) {
					_ = s.Store.Delivered(ctx, item.Command.ID)
					continue
				}
				lastErr = e
				continue
			}
		}
		if e = s.Store.Delivered(ctx, item.Command.ID); e != nil {
			return e
		}
	}
	return lastErr
}

func (s *Service) Webhook(secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		want := sha256.Sum256([]byte(secret))
		got := sha256.Sum256([]byte(r.Header.Get("X-Gitlab-Token")))
		if secret == "" || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if _, e := io.Copy(io.Discard, r.Body); e != nil {
			http.Error(w, "payload too large", 413)
			return
		}
		select {
		case s.Wake <- struct{}{}:
		default:
		}
		w.WriteHeader(202)
	})
}
func (s *Service) Scan(ctx context.Context) error {
	projects, e := s.Store.Projects(ctx)
	if e != nil {
		return e
	}
	for _, p := range projects {
		if !p.Enabled {
			continue
		}
		token, e := s.ResolveCredential(p.CredentialRef)
		if e != nil {
			slog.Error("project credential unavailable", "project", p.ID)
			continue
		}
		g := gitlab.New(p.GitLabURL, token)
		for _, repo := range p.Repositories {
			issues, e := g.ListEligibleIssues(ctx, repo.ProjectID)
			if e != nil {
				slog.Error("issue scan", "project", p.ID, "repo", repo.ID, "error", e)
				continue
			}
			for _, issue := range issues {
				seen, e := s.Store.HasSource(ctx, p.GitLabURL, issue)
				if e != nil {
					return e
				}
				if seen {
					continue
				}
				legacy := false
				for _, l := range issue.Labels {
					if l == "ai:working" || l == "ai:mr-opened" || l == "ai:needs-human" {
						legacy = true
					}
				}
				if legacy {
					continue
				}
				now := time.Now().UTC()
				t := domain.Task{ID: ID(), Project: p, Issue: issue, Stage: "queued", Executions: map[string]domain.Execution{}, CreatedAt: now, UpdatedAt: now}
				if e = s.Store.CreateTask(ctx, t); e != nil && !errors.Is(e, store.ErrConflict) {
					return e
				}
			}
		}
	}
	return nil
}
func (s *Service) Sync(ctx context.Context, a *pipeline.Activities) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	last := map[string]string{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			tasks, e := s.Store.Tasks(ctx)
			if e != nil {
				continue
			}
			for _, t := range tasks {
				stamp := fmt.Sprint(t.UpdatedAt)
				if last[t.ID] == stamp {
					continue
				}
				if e = a.Sync(ctx, t); e != nil {
					slog.Warn("GitLab progress pending retry", "task", t.ID, "error", e)
					continue
				}
				last[t.ID] = stamp
			}
		}
	}
}
