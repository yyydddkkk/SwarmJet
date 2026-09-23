package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"swarmjet/internal/domain"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/imagebuild"
	"swarmjet/internal/pipeline"
	"swarmjet/internal/store"
)

// InitReadiness reports whether a managed project has completed initialization
// verification. It gates issue scanning so a project that is not ready can never
// accept work, even if it was enabled earlier.
type InitReadiness interface {
	ReadyForTasks(ctx context.Context, projectID string) bool
}

type Service struct {
	DataDir           string
	Store             *store.Store
	Temporal          client.Client
	ResolveCredential func(string) (string, error)
	ProjectCredential func(domain.Project, bool) (string, error)
	Initializer       InitReadiness
	RequireNode       bool
	Wake              chan struct{}
}

func (s *Service) projectCredential(p domain.Project) (string, error) {
	if s.ProjectCredential != nil {
		return s.ProjectCredential(p, false)
	}
	return s.ResolveCredential(p.CredentialReference())
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
	enabled, e := s.Store.EnabledAgents(ctx)
	if e != nil {
		return e
	}
	for _, p := range projects {
		if !p.Enabled {
			continue
		}
		if p.ManagedMeta() && (s.Initializer == nil || !s.Initializer.ReadyForTasks(ctx, p.ID)) {
			slog.Warn("managed project initialization not ready; skipping issue scan", "project", p.ID)
			continue
		}
		token, e := s.projectCredential(p)
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
					if err := s.scanFollowup(ctx, g, p, issue, enabled); err != nil {
						slog.Warn("issue follow-up scan", "project", p.ID, "issue", issue.IID, "error", err)
					}
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
				taskProject, resolveErr := domain.ResolveIssueAgent(p, issue, enabled)
				if resolveErr != nil {
					slog.Warn("issue agent rejected", "project", p.ID, "issue", issue.IID, "error", resolveErr)
					if err := g.UpsertProgress(ctx, issue, "dispatch", "无法接单："+resolveErr.Error()+"。修正标签或启用配置后将自动重试。"); err != nil {
						slog.Warn("dispatch feedback failed", "error", err)
					}
					continue
				}
				if !s.RequireNode && taskProject.Nodes.Mode == "" {
					taskProject, e = imagebuild.ApplyToProject(s.DataDir, taskProject)
					if e != nil {
						return e
					}
				}
				now := time.Now().UTC()
				t := domain.Task{ID: ID(), Project: taskProject, Issue: issue, Stage: "queued", Executions: map[string]domain.Execution{}, CreatedAt: now, UpdatedAt: now}
				if _, e = s.Store.CreateTaskScheduled(ctx, t, s.RequireNode); e != nil {
					if errors.Is(e, store.ErrNoNode) {
						// Distributed mode: leave the issue unclaimed until a node has capacity.
						slog.Warn("no execution node available; task deferred", "project", p.ID, "issue", issue.IID)
						continue
					}
					if !errors.Is(e, store.ErrConflict) {
						return e
					}
				}
			}
		}
	}
	return nil
}

func runComment(body string) (string, bool) {
	body = strings.TrimSpace(body)
	rest, ok := strings.CutPrefix(body, "/swarmjet run")
	if !ok || rest != "" && rest[0] != ' ' && rest[0] != '\n' && rest[0] != '\t' && rest[0] != '\r' {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

func (s *Service) scanFollowup(ctx context.Context, g *gitlab.Client, p domain.Project, issue domain.Issue, enabled []string) error {
	first, err := s.Store.FirstTaskCreatedAt(ctx, p.GitLabURL, issue)
	if err != nil {
		return err
	}
	enabledAt, err := s.Store.IssueRoundEnabledAt(ctx)
	if err != nil {
		return err
	}
	notes, err := g.ListIssueNotes(ctx, issue)
	if err != nil {
		return err
	}
	for _, note := range notes {
		if note.ID < 1 || note.System || note.Internal || note.CreatedAt.Before(first) || note.CreatedAt.Before(enabledAt) || !note.UpdatedAt.IsZero() && !note.UpdatedAt.Equal(note.CreatedAt) {
			continue
		}
		body, ok := runComment(note.Body)
		if !ok {
			continue
		}
		if err := s.Store.RecordIssueRunCommand(ctx, p.GitLabURL, issue, note.ID, body, note.CreatedAt); err != nil {
			return err
		}
	}
	pending, err := s.Store.PendingIssueRunCommands(ctx, p.GitLabURL, issue)
	if err != nil || !pending {
		return err
	}
	previous, err := s.Store.LatestTaskForIssue(ctx, p.GitLabURL, issue)
	if err != nil || !domain.Terminal(previous.Stage) {
		return err
	}
	taskProject, err := domain.ResolveIssueAgent(p, issue, enabled)
	if err != nil {
		return err
	}
	if !s.RequireNode && taskProject.Nodes.Mode == "" {
		taskProject, err = imagebuild.ApplyToProject(s.DataDir, taskProject)
		if err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	t := domain.Task{ID: ID(), Project: taskProject, Issue: issue, Stage: "queued", Executions: map[string]domain.Execution{}, TriggerNoteIDs: []int64{}, CreatedAt: now, UpdatedAt: now}
	_, err = s.Store.CreateTaskScheduled(ctx, t, s.RequireNode)
	if errors.Is(err, store.ErrNoNode) || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}
func (s *Service) Sync(ctx context.Context, a *pipeline.Activities) {
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			tasks, e := s.Store.PendingGitLabSync(ctx)
			if e != nil {
				continue
			}
			for _, item := range tasks {
				if e = a.Sync(ctx, item.Task); e != nil {
					slog.Warn("GitLab progress pending retry", "task", item.Task.ID, "error", e)
					_ = s.Store.DelayGitLabSync(ctx, item.Task.ID)
					continue
				}
				if e = s.Store.CompleteGitLabSync(ctx, item.Task.ID, item.UpdatedAt); e != nil {
					slog.Warn("GitLab progress sync checkpoint failed", "task", item.Task.ID, "error", e)
				}
			}
		}
	}
}
