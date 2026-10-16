package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
	"github.com/yyydddkkk/SwarmJet/internal/gitlab"
	"github.com/yyydddkkk/SwarmJet/internal/intake"
	"github.com/yyydddkkk/SwarmJet/internal/labels"
	"github.com/yyydddkkk/SwarmJet/internal/mq"
	"github.com/yyydddkkk/SwarmJet/internal/observability"
	"github.com/yyydddkkk/SwarmJet/internal/pipeline"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// InitReadiness reports whether a managed project has completed initialization
// verification. It gates issue scanning so a project that is not ready can never
// accept work, even if it was enabled earlier.
type InitReadiness interface {
	ReadyForTasks(ctx context.Context, projectID string) bool
}

type Service struct {
	ForgeClient      func(domain.Project, bool) (forge.Client, error)
	DataDir          string
	Store            *store.Store
	Metrics          *observability.Metrics
	WebhookPublisher interface {
		Publish(context.Context, string, []byte, string) error
	}
	GitLabSigningToken            string
	Temporal                      client.Client
	CommandPublisher              CommandPublisher
	ResolveCredential             func(string) (string, error)
	ProjectCredential             func(domain.Project, bool) (string, error)
	Initializer                   InitReadiness
	RequireNode                   bool
	Wake                          chan struct{}
	LabelsWake                    chan struct{}
	scanMu                        sync.Mutex
}

func (s *Service) intakeOptions(projectID string, claim bool) intake.Options {
	return intake.Options{DataDir: s.DataDir, RequireNode: s.RequireNode, ClaimRunCommands: claim}
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
		maintenance, checkErr := s.Store.UpgradeMaintenance(ctx)
		if checkErr != nil || maintenance {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				continue
			}
		}
		admitted, err := s.Store.AdmitPendingTasks(ctx, 20)
		if s.Metrics != nil {
			s.Metrics.Admission("local_or_node", admitted, err)
		}
		if err != nil && ctx.Err() == nil {
			slog.Error("task admission", "error", err)
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
		var err error
		if s.CommandPublisher != nil {
			err = s.PublishCommands(ctx, s.CommandPublisher)
		} else {
			err = s.Deliver(ctx)
		}
		if err != nil && ctx.Err() == nil {
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
	maintenance, err := s.Store.UpgradeMaintenance(ctx)
	if err != nil {
		return err
	}
	if maintenance {
		return nil
	}
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
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		body, e := io.ReadAll(r.Body)
		if e != nil {
			http.Error(w, "payload too large", 413)
			return
		}
		if s.GitLabSigningToken != "" && r.Header.Get("webhook-signature") != "" {
			if !verifyGitLabSignature(s.GitLabSigningToken, r.Header, body) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		} else {
			want := sha256.Sum256([]byte(secret))
			got := sha256.Sum256([]byte(r.Header.Get("X-Gitlab-Token")))
			if secret == "" || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if !s.acceptWebhook(w, r, "gitlab", deliveryID(r.Header.Get("webhook-id"), r.Header.Get("Idempotency-Key")), r.Header.Get("X-Gitlab-Event"), body) {
			return
		}
		w.WriteHeader(202)
	})
}

func verifyGitLabSignature(token string, header http.Header, body []byte) bool {
	if !strings.HasPrefix(token, "whsec_") {
		return false
	}
	key, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(token, "whsec_"))
	if err != nil || len(key) == 0 {
		return false
	}
	id, stamp := header.Get("webhook-id"), header.Get("webhook-timestamp")
	when, err := strconv.ParseInt(stamp, 10, 64)
	if id == "" || err != nil || time.Since(time.Unix(when, 0)) > 10*time.Minute || time.Until(time.Unix(when, 0)) > 10*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(id + "." + stamp + "."))
	_, _ = mac.Write(body)
	want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for _, candidate := range strings.Fields(header.Get("webhook-signature")) {
		if hmac.Equal([]byte(want), []byte(candidate)) {
			return true
		}
	}
	return false
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
		w.WriteHeader(http.StatusAccepted)
	})
}

func deliveryID(candidates ...string) string {
	for _, candidate := range candidates {
		if candidate != "" && len(candidate) <= 200 && strings.TrimSpace(candidate) == candidate {
			return candidate
		}
	}
	return ""
}

func (s *Service) acceptWebhook(w http.ResponseWriter, r *http.Request, provider, id, eventType string, body []byte) bool {
	if id == "" {
		http.Error(w, "missing delivery ID", http.StatusBadRequest)
		return false
	}
	if s.WebhookPublisher == nil {
		http.Error(w, "webhook queue unavailable", http.StatusServiceUnavailable)
		return false
	}
	if len(eventType) > 200 {
		http.Error(w, "event type too long", http.StatusBadRequest)
		return false
	}
	message, err := json.Marshal(WebhookMessage{Provider: provider, DeliveryID: id, EventType: eventType, Payload: body})
	if err != nil {
		http.Error(w, "invalid webhook", http.StatusBadRequest)
		return false
	}
	publishCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	err = s.WebhookPublisher.Publish(publishCtx, mq.WebhookQueue, message, provider+":"+id)
	if err != nil {
		slog.Error("webhook publish", "provider", provider, "delivery_id", id, "error", err)
		http.Error(w, "webhook queue unavailable", http.StatusServiceUnavailable)
		return false
	}
	slog.Info("webhook published", "provider", provider, "delivery_id", id)
	return true
}
func (s *Service) Scan(ctx context.Context) error {
	start := time.Now()
	err := s.scan(ctx, "", 0)
	if s.Metrics != nil {
		s.Metrics.Scan("all", time.Since(start), err)
	}
	return err
}

func (s *Service) ScanRepository(ctx context.Context, provider domain.Provider, repoID int64) error {
	start := time.Now()
	err := s.scan(ctx, provider, repoID)
	if s.Metrics != nil {
		s.Metrics.Scan("repository", time.Since(start), err)
	}
	return err
}

func (s *Service) scan(ctx context.Context, provider domain.Provider, repoID int64) error {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	maintenance, err := s.Store.UpgradeMaintenance(ctx)
	if err != nil {
		return err
	}
	if maintenance {
		return nil
	}
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
		if p.HostingProvider() == domain.ProviderGitHub && !p.GitHubAutoIntake {
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
			scanErr := s.Store.WithRepositoryScanLock(ctx, string(p.HostingProvider()), repo.ProjectID, func() error {
				issues, e := g.ListEligibleIssues(ctx, repo.ProjectID)
				if e != nil {
					slog.Error("issue scan", "project", p.ID, "repo", repo.ID, "error", e)
					return fmt.Errorf("project %s repo %s issue scan: %w", p.ID, repo.ID, e)
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
					authorized, e := s.authorizeInitialTrigger(ctx, g, p, issue)
					if e != nil {
						return e
					}
					if !authorized {
						continue
					}
					task, e := intake.Schedule(ctx, s.Store, p, issue, enabled, s.intakeOptions(p.ID, false))
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
					logTaskScheduled(ctx, task)
					s.recordScheduledAdmission(task)
				}
				return nil
			})
			if scanErr != nil {
				scanErrors = append(scanErrors, scanErr)
			}
		}
	}
	return errors.Join(scanErrors...)
}

func logTaskScheduled(ctx context.Context, task domain.Task) {
	attrs := []any{"task_id", task.ID, "project_id", task.Project.ID, "provider", task.Project.HostingProvider(), "repository_id", task.Issue.ProjectID, "issue_iid", task.Issue.IID, "admission_backend", task.AdmissionBackend, "admission_status", task.AdmissionStatus}
	if delivery, ok := ctx.Value(webhookDeliveryContextKey{}).(webhookDeliveryContext); ok {
		attrs = append(attrs, "webhook_provider", delivery.provider, "delivery_id", delivery.deliveryID)
	}
	slog.Info("task scheduled", attrs...)
}

func (s *Service) recordScheduledAdmission(task domain.Task) {
	if s.Metrics != nil && task.AdmissionStatus == "assigned" {
		s.Metrics.Admitted(task.AdmissionBackend)
	}
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
		authorized, err := s.authorizeRunNote(ctx, g, p, issue, note)
		if err != nil {
			return err
		}
		if !authorized {
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
	task, err := intake.Schedule(ctx, s.Store, p, issue, enabled, s.intakeOptions(p.ID, true))
	if errors.Is(err, store.ErrNoNode) || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err == nil {
		logTaskScheduled(ctx, task)
		s.recordScheduledAdmission(task)
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
