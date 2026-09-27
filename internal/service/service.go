package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"swarmjet/internal/domain"
	"swarmjet/internal/forge"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/intake"
	"swarmjet/internal/labels"
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
	ForgeClient                   func(domain.Project, bool) (forge.Client, error)
	DataDir                       string
	Store                         *store.Store
	Temporal                      client.Client
	ResolveCredential             func(string) (string, error)
	ProjectCredential             func(domain.Project, bool) (string, error)
	Initializer                   InitReadiness
	RequireNode                   bool
	KubernetesPool                string
	KubernetesValidationProjectID string
	KubernetesCapacity            int
	RunnerResources               domain.RunnerResources
	RunnerImage                   string
	Wake                          chan struct{}
	LabelsWake                    chan struct{}
	scanMu                        sync.Mutex
}

func (s *Service) intakeOptions(projectID string, claim bool) intake.Options {
	options := intake.Options{DataDir: s.DataDir, RequireNode: s.RequireNode, ClaimRunCommands: claim}
	if s.KubernetesPool != "" && projectID == s.KubernetesValidationProjectID {
		options.KubernetesPool = s.KubernetesPool
		options.RunnerResources = s.RunnerResources
		options.RunnerImage = s.RunnerImage
	}
	return options
}

// SyncLabels reconciles selectable labels independently of issue scanning.
func (s *Service) SyncLabels(ctx context.Context) {
	tick := time.NewTicker(2 * time.Minute)
	defer tick.Stop()
	for {
		if err := s.reconcileLabels(ctx); err != nil {
			slog.Warn("GitLab label reconciliation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.LabelsWake:
		}
	}
}

func (s *Service) reconcileLabels(ctx context.Context) error {
	projects, err := s.Store.Projects(ctx)
	if err != nil {
		return err
	}
	enabled, err := s.Store.EnabledAgents(ctx)
	if err != nil {
		return err
	}
	wanted := labels.Selection(enabled)
	for _, p := range projects {
		if !p.Enabled || p.ManagedMeta() && (s.Initializer == nil || !s.Initializer.ReadyForTasks(ctx, p.ID)) {
			continue
		}
		client, err := s.client(p)
		if err != nil {
			slog.Warn("GitLab label credential unavailable", "project", p.ID, "error", err)
			continue
		}
		for _, repo := range p.Repositories {
			if _, err := labels.Ensure(ctx, client, repo.ProjectID, wanted); err != nil {
				slog.Warn("GitLab label setup failed", "project", p.ID, "repo", repo.ID, "error", err)
			}
		}
	}
	return nil
}

func (s *Service) projectCredential(p domain.Project) (string, error) {
	if s.ProjectCredential != nil {
		return s.ProjectCredential(p, false)
	}
	return s.ResolveCredential(p.CredentialReference())
}

func (s *Service) client(p domain.Project) (forge.Client, error) {
	if s.ForgeClient != nil {
		return s.ForgeClient(p, false)
	}
	token, err := s.projectCredential(p)
	if err != nil {
		return nil, err
	}
	return gitlab.New(p.GitLabURL, token), nil
}

func (s *Service) Run(ctx context.Context) {
	go s.runOutbox(ctx)
	go s.runInbox(ctx)
	go s.runAdmission(ctx)
	slow := time.NewTicker(120 * time.Second)
	defer slow.Stop()
	if e := s.Scan(ctx); e != nil {
		slog.Error("scan", "error", e)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-slow.C:
			if e := s.Scan(ctx); e != nil {
				slog.Error("scan", "error", e)
			}
		}
	}
}

func (s *Service) runAdmission(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		if _, err := s.Store.AdmitPendingTasks(ctx, 20); err != nil && ctx.Err() == nil {
			slog.Error("task admission", "error", err)
		}
		if s.KubernetesPool != "" {
			if _, err := s.Store.AdmitKubernetesTasks(ctx, s.KubernetesPool, s.KubernetesCapacity, 20); err != nil && ctx.Err() == nil {
				slog.Error("Kubernetes task admission", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Service) runOutbox(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		if err := s.Deliver(ctx); err != nil && ctx.Err() == nil {
			slog.Error("outbox delivery", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Service) runInbox(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	for {
		if err := s.ProcessInbox(ctx); err != nil && ctx.Err() == nil {
			slog.Error("webhook inbox processing", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.Wake:
		case <-tick.C:
		case <-prune.C:
			if err := s.Store.PruneWebhookInbox(ctx); err != nil {
				slog.Error("webhook inbox pruning", "error", err)
			}
		}
	}
}

func (s *Service) ProcessInbox(ctx context.Context) error {
	deliveries, err := s.Store.ClaimWebhookInbox(ctx, 100)
	if err != nil || len(deliveries) == 0 {
		return err
	}
	type target struct {
		provider domain.Provider
		repoID   int64
	}
	groups := map[target][]int64{}
	for _, delivery := range deliveries {
		provider, repoID := webhookRepository(delivery.Provider, delivery.Payload)
		groups[target{provider, repoID}] = append(groups[target{provider, repoID}], delivery.ID)
	}
	var failures []error
	for key, ids := range groups {
		if key.repoID == 0 {
			err = s.Scan(ctx)
		} else {
			err = s.ScanRepository(ctx, key.provider, key.repoID)
		}
		if err != nil {
			failures = append(failures, err)
			if retryErr := s.Store.RetryWebhookInbox(ctx, ids, 10*time.Second); retryErr != nil {
				failures = append(failures, retryErr)
			}
			continue
		}
		if err = s.Store.CompleteWebhookInbox(ctx, ids); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func webhookRepository(provider string, payload []byte) (domain.Provider, int64) {
	var event struct {
		Project struct {
			ID int64 `json:"id"`
		} `json:"project"`
		ProjectID  int64 `json:"project_id"`
		Repository struct {
			ID int64 `json:"id"`
		} `json:"repository"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return "", 0
	}
	switch provider {
	case "gitlab":
		if event.Project.ID > 0 {
			return domain.ProviderGitLab, event.Project.ID
		}
		if event.ProjectID > 0 {
			return domain.ProviderGitLab, event.ProjectID
		}
	case "github":
		if event.Repository.ID > 0 {
			return domain.ProviderGitHub, event.Repository.ID
		}
	}
	return "", 0
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
			name, queue := workflowRoute(t)
			_, e = s.Temporal.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: t.ID, TaskQueue: queue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE}, name, t)
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

func workflowRoute(t domain.Task) (name, queue string) {
	if t.AdmissionBackend == "kubernetes" {
		return pipeline.KubernetesWorkflowName, pipeline.KubernetesQueue
	}
	return pipeline.WorkflowName, pipeline.Queue
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
		body, e := io.ReadAll(r.Body)
		if e != nil {
			http.Error(w, "payload too large", 413)
			return
		}
		if !s.acceptWebhook(w, r, "gitlab", deliveryID(r.Header.Get("webhook-id"), r.Header.Get("Idempotency-Key")), r.Header.Get("X-Gitlab-Event"), body) {
			return
		}
		select {
		case s.Wake <- struct{}{}:
		default:
		}
		w.WriteHeader(202)
	})
}

// GitHubWebhook verifies the raw request body before waking the issue scanner.
// The periodic scan remains the fallback when a deployment has no public ingress.
func (s *Service) GitHubWebhook(secret string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if secret == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		got := r.Header.Get("X-Hub-Signature-256")
		if !hmac.Equal([]byte(want), []byte(got)) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !s.acceptWebhook(w, r, "github", deliveryID(r.Header.Get("X-GitHub-Delivery")), r.Header.Get("X-GitHub-Event"), body) {
			return
		}
		select {
		case s.Wake <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusAccepted)
	})
}

func deliveryID(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate != "" && len(candidate) <= 200 {
			return candidate
		}
	}
	// Missing platform IDs must not collapse distinct deliveries by body hash.
	var random [16]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(random[:])
}

func (s *Service) acceptWebhook(w http.ResponseWriter, r *http.Request, provider, id, eventType string, body []byte) bool {
	if id == "" || s.Store == nil {
		http.Error(w, "webhook inbox unavailable", http.StatusServiceUnavailable)
		return false
	}
	if len(eventType) > 200 {
		eventType = eventType[:200]
	}
	err := s.Store.EnqueueWebhook(r.Context(), provider, id, eventType, body)
	if errors.Is(err, store.ErrWebhookInboxFull) {
		http.Error(w, "webhook inbox full", http.StatusServiceUnavailable)
		return false
	}
	if err != nil {
		slog.Error("webhook inbox insert", "provider", provider, "error", err)
		http.Error(w, "webhook inbox unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}
func (s *Service) Scan(ctx context.Context) error {
	return s.scan(ctx, "", 0)
}

func (s *Service) ScanRepository(ctx context.Context, provider domain.Provider, repoID int64) error {
	return s.scan(ctx, provider, repoID)
}

func (s *Service) scan(ctx context.Context, provider domain.Provider, repoID int64) error {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	var scanErrors []error
	projects, e := s.Store.Projects(ctx)
	if e != nil {
		return e
	}
	enabled, e := s.Store.EnabledAgents(ctx)
	if e != nil {
		return e
	}
	for _, p := range projects {
		if !p.Enabled || provider != "" && p.HostingProvider() != provider {
			continue
		}
		if repoID > 0 {
			found := false
			for _, repo := range p.Repositories {
				if repo.ProjectID == repoID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if !intake.ReadyForTasks(ctx, p, s.Initializer) {
			slog.Warn("managed project initialization not ready; skipping issue scan", "project", p.ID)
			continue
		}
		g, e := s.client(p)
		if e != nil {
			slog.Error("project credential unavailable", "project", p.ID)
			scanErrors = append(scanErrors, fmt.Errorf("project %s credential: %w", p.ID, e))
			continue
		}
		for _, repo := range p.Repositories {
			if repoID > 0 && repo.ProjectID != repoID {
				continue
			}
			issues, e := g.ListEligibleIssues(ctx, repo.ProjectID)
			if e != nil {
				slog.Error("issue scan", "project", p.ID, "repo", repo.ID, "error", e)
				scanErrors = append(scanErrors, fmt.Errorf("project %s repo %s issue scan: %w", p.ID, repo.ID, e))
				continue
			}
			for _, issue := range issues {
				seen, e := s.Store.HasSource(ctx, p.HostingURL(), issue)
				if e != nil {
					return e
				}
				if seen {
					if err := s.scanFollowup(ctx, g, p, issue, enabled); err != nil {
						slog.Warn("issue follow-up scan", "project", p.ID, "issue", issue.IID, "error", err)
						scanErrors = append(scanErrors, fmt.Errorf("project %s issue %d follow-up: %w", p.ID, issue.IID, err))
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
				_, e = intake.Schedule(ctx, s.Store, p, issue, enabled, s.intakeOptions(p.ID, false))
				if e != nil {
					if errors.Is(e, store.ErrNoNode) {
						// Distributed mode: leave the issue unclaimed until a node has capacity.
						slog.Warn("no execution node available; task deferred", "project", p.ID, "issue", issue.IID)
						continue
					}
					if errors.Is(e, store.ErrConflict) {
						continue
					}
					var agentError *intake.AgentSelectionError
					if !errors.As(e, &agentError) {
						return e
					}
					slog.Warn("issue agent rejected", "project", p.ID, "issue", issue.IID, "error", e)
					if err := g.UpsertProgress(ctx, issue, "dispatch", "无法接单："+e.Error()+"。修正标签或启用配置后将自动重试。"); err != nil {
						slog.Warn("dispatch feedback failed", "error", err)
					}
					continue
				}
			}
		}
	}
	return errors.Join(scanErrors...)
}

func runComment(body string) (string, bool) {
	body = strings.TrimSpace(body)
	rest, ok := strings.CutPrefix(body, "/swarmjet run")
	if !ok || rest != "" && rest[0] != ' ' && rest[0] != '\n' && rest[0] != '\t' && rest[0] != '\r' {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

func (s *Service) scanFollowup(ctx context.Context, g forge.Client, p domain.Project, issue domain.Issue, enabled []string) error {
	first, err := s.Store.FirstTaskCreatedAt(ctx, p.HostingURL(), issue)
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
		if err := s.Store.RecordIssueRunCommand(ctx, p.HostingURL(), issue, note.ID, body, note.CreatedAt); err != nil {
			return err
		}
	}
	pending, err := s.Store.PendingIssueRunCommands(ctx, p.HostingURL(), issue)
	if err != nil || !pending {
		return err
	}
	previous, err := s.Store.LatestTaskForIssue(ctx, p.HostingURL(), issue)
	if err != nil || !domain.Terminal(previous.Stage) {
		return err
	}
	_, err = intake.Schedule(ctx, s.Store, p, issue, enabled, s.intakeOptions(p.ID, true))
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
