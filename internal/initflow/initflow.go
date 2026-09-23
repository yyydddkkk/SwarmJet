// Package initflow owns the durable, resumable initialization of a managed
// Meta-Repo. It is a persisted state machine, not a blocking HTTP request: the
// worker coordinator advances each project, survives restarts, and can be
// cancelled, retried and given missing information.
package initflow

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"swarmjet/internal/domain"
	"swarmjet/internal/execution"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/managedmeta"
	"swarmjet/internal/store"
)

// Repository is the persistence subset the coordinator needs. *store.Store
// implements it; tests use an in-memory fake.
type Repository interface {
	Project(ctx context.Context, id string) (domain.Project, error)
	SaveProject(ctx context.Context, p domain.Project) (domain.Project, error)
	Initialization(ctx context.Context, id string) (domain.Initialization, error)
	Initializations(ctx context.Context) ([]domain.Initialization, error)
	SaveInitialization(ctx context.Context, init domain.Initialization) error
	Lease(ctx context.Context, name, owner string, ttl time.Duration) (bool, error)
	ReleaseLeases(ctx context.Context, owner string) error
}

// Cloner materializes one repository at an exact commit into a new directory.
type Cloner func(ctx context.Context, project domain.Project, repo domain.Repository, dest, sha string) error

// Runner executes one verification command and returns its exit code and a
// bounded combined output. A nil error means the command ran; a non-zero code
// still reports a normal (failed) verification result.
type Runner func(ctx context.Context, dir string, argv []string) (int, string, error)

type Manager struct {
	CancelAnalysis func(context.Context, domain.Initialization) error
	RemoteAnalysis func(context.Context, *domain.Initialization, domain.Project) (execution.AnalysisResult, error)
	AnalysisEnv    map[string]string

	Store      Repository
	DataDir    string
	Agents     execution.AgentBackend
	Managed    *managedmeta.Manager
	Credential func(string) (string, error)
	// Clients builds a GitLab client. Defaults to the project credential.
	Clients func(domain.Project) (*gitlab.Client, error)
	// Cloner materializes a pinned revision. Defaults to git.
	Cloner Cloner
	// Runner executes verification. Defaults to a bounded host process.
	Runner Runner
	// Docker runs a docker CLI command for isolated verification and container
	// cleanup. Tests inject a fake; production uses the docker binary.
	Docker func(context.Context, []string) ([]byte, error)
	// VerifyImage, when non-empty, runs verification inside this image with no
	// network instead of directly on the worker host.
	VerifyImage string
	// VerifyTimeout bounds each verification command. Defaults to 10 minutes.
	VerifyTimeout time.Duration
	Poll          time.Duration
	Logger        *slog.Logger
	// CredentialFor resolves a project token together with its GitLab address
	// binding. When nil, Credential is used without an address check.
	CredentialFor func(domain.Project) (string, error)

	automationOwner string
	ownerOnce       sync.Once
}

func InitID(projectID string) string { return "init-" + projectID }

func (m *Manager) log(msg string, err error) {
	if m.Logger != nil {
		m.Logger.Warn(msg, "error", err)
	}
}

func (m *Manager) initDir(projectID string) string {
	return filepath.Join(m.DataDir, "initializations", projectID)
}

var errLeaseLost = errors.New("initialization lease lost")

type guardKey struct{}

func withGuard(ctx context.Context, g *leaseGuard) context.Context {
	return context.WithValue(ctx, guardKey{}, g)
}

func guardFrom(ctx context.Context) *leaseGuard {
	g, _ := ctx.Value(guardKey{}).(*leaseGuard)
	return g
}

// leaseGuard tracks ownership of a project initialization for one execution.
// A step that loses the lease must never persist its result.
type leaseGuard struct {
	name  string
	owner string
	store Repository
	ttl   time.Duration
	lost  atomic.Bool
}

func (g *leaseGuard) held() bool { return !g.lost.Load() }

func (g *leaseGuard) renew(ctx context.Context) bool {
	ok, err := g.store.Lease(ctx, g.name, g.owner, g.ttl)
	if err != nil || !ok {
		g.lost.Store(true)
		return false
	}
	return true
}

// executionOwner is unique per worker process. Every worker sharing a project
// must present a different owner, otherwise Store.Lease treats them as the same
// holder and allows several executors into the same initialization.
func (m *Manager) executionOwner() string {
	m.ownerOnce.Do(func() {
		host, _ := os.Hostname()
		random := make([]byte, 8)
		_, _ = rand.Read(random)
		m.automationOwner = fmt.Sprintf("%s-%d-%x", host, os.Getpid(), random)
	})
	return m.automationOwner
}

func (m *Manager) client(project domain.Project) (*gitlab.Client, error) {
	if m.Clients != nil {
		return m.Clients(project)
	}
	token, err := m.projectCredential(project)
	if err != nil {
		return nil, err
	}
	return gitlab.New(project.GitLabURL, token), nil
}

func (m *Manager) projectCredential(project domain.Project) (string, error) {
	if m.CredentialFor != nil {
		return m.CredentialFor(project)
	}
	if m.Credential == nil {
		return "", errors.New("credential resolver is not configured")
	}
	return m.Credential(project.CredentialReference())
}

// Run resumes non-terminal initializations after restart and advances running
// agents. A per-project lease guarantees a single executor per project.
func (m *Manager) Run(ctx context.Context) {
	poll := m.Poll
	if poll <= 0 {
		poll = 3 * time.Second
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		m.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) sweep(ctx context.Context) {
	list, err := m.Store.Initializations(ctx)
	if err != nil {
		m.log("list initializations", err)
		return
	}
	for _, init := range list {
		switch {
		case init.Status == domain.InitReady && init.Publication != nil && init.Publication.Status == "pending":
			m.advanceWithLease(ctx, init)
		case init.Status == domain.InitCancelled:
			m.stopCancelled(ctx, init)
		case init.Status == domain.InitNeedsInput || init.Status == domain.InitBlocked:
			// Waiting for an operator answer or an explicit retry. Polling must
			// not restart the agent or re-run verification on its own.
			continue
		case domain.InitTerminal(init.Status):
			continue
		default:
			m.advanceWithLease(ctx, init)
		}
	}
}

func (m *Manager) stopCancelled(ctx context.Context, init domain.Initialization) {
	// Verification containers are owned by this coordinator, not the agent
	// backend, so they are cleaned even when the agent was already stopped.
	m.cleanupVerifyContainers(init.ProjectID)
	if init.AgentStopped || (m.Agents == nil && m.CancelAnalysis == nil) {
		return
	}
	if err := m.cancelAnalysis(ctx, init); err != nil {
		m.log("stop cancelled initialization "+init.ProjectID, err)
		return
	}
	init.AgentStopped = true
	init.Version++
	if err := m.Store.SaveInitialization(ctx, init); err != nil {
		m.log("mark initialization stopped", err)
	}
}

// advanceWithLease holds a per-project lease for the whole step and renews it
// while a long verification runs. It cancels the step as soon as another actor
// changes the record, and a step whose lease is lost never writes its result.
func (m *Manager) advanceWithLease(ctx context.Context, init domain.Initialization) {
	name := "initialization:" + init.ProjectID
	owner := m.executionOwner()
	ttl := 45 * time.Second
	ok, err := m.Store.Lease(ctx, name, owner, ttl)
	if err != nil || !ok {
		return
	}
	guard := &leaseGuard{name: name, owner: owner, store: m.Store, ttl: ttl}
	advanceCtx, cancel := context.WithCancel(withGuard(ctx, guard))
	defer cancel()
	defer m.Store.ReleaseLeases(context.WithoutCancel(ctx), owner)
	done := make(chan struct{})
	go func() {
		renew := time.NewTicker(ttl / 3)
		watch := time.NewTicker(5 * time.Second)
		defer renew.Stop()
		defer watch.Stop()
		for {
			select {
			case <-done:
				return
			case <-advanceCtx.Done():
				return
			case <-renew.C:
				if !guard.renew(advanceCtx) {
					cancel()
					return
				}
			case <-watch.C:
				current, err := m.Store.Initialization(advanceCtx, init.ID)
				if err != nil {
					continue
				}
				if current.Status == domain.InitCancelled || current.Version != init.Version {
					guard.lost.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	if _, err := m.Advance(advanceCtx, init); err != nil && !errors.Is(err, errLeaseLost) && !errors.Is(err, context.Canceled) {
		m.log("advance initialization "+init.ProjectID, err)
	}
	close(done)
}

// Ensure creates the durable initialization record if absent and is idempotent
// across repeated calls, so a retried start cannot create a second Meta-Repo.
func (m *Manager) Ensure(ctx context.Context, projectID string) (domain.Initialization, error) {
	project, err := m.Store.Project(ctx, projectID)
	if err != nil {
		return domain.Initialization{}, err
	}
	if !project.ManagedMeta() {
		return domain.Initialization{}, errors.New("该项目的 Meta-Repo 由外部管理，无需系统初始化")
	}
	init, err := m.Store.Initialization(ctx, InitID(projectID))
	if err == nil {
		if init.Status == domain.InitCancelled {
			init.Status = domain.InitPending
			init.Stage = "connect"
			init.Error = ""
			init.Questions = nil
			init.Message = "重新开始初始化"
			return m.save(ctx, init, project)
		}
		return init, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return domain.Initialization{}, err
	}
	now := time.Now().UTC()
	init = domain.Initialization{ID: InitID(projectID), ProjectID: projectID, Status: domain.InitPending, Stage: "connect", Message: "已创建初始化任务", RepositorySHAs: map[string]string{}, CreatedAt: now, UpdatedAt: now}
	saved, err := m.save(ctx, init, project)
	if errors.Is(err, store.ErrConflict) {
		// Another actor created the record first; return the stored one.
		return m.Store.Initialization(ctx, InitID(projectID))
	}
	return saved, err
}

func (m *Manager) Get(ctx context.Context, projectID string) (domain.Initialization, error) {
	return m.Store.Initialization(ctx, InitID(projectID))
}

// Cancel stops a live analysis and marks the job cancelled. Saved connections,
// the analysis copy and the managed Meta-Repo are retained.
func (m *Manager) Cancel(ctx context.Context, projectID string) error {
	init, err := m.Store.Initialization(ctx, InitID(projectID))
	if err != nil {
		return err
	}
	if domain.InitTerminal(init.Status) {
		return nil
	}
	if m.Agents != nil || m.CancelAnalysis != nil {
		if err = m.cancelAnalysis(ctx, init); err != nil {
			return err
		}
	}
	init.Status = domain.InitCancelled
	init.Stage = "cancelled"
	init.AgentStopped = false
	init.Message = "初始化已取消，连接、分析结果与受管 Meta-Repo 保留"
	project, _ := m.Store.Project(ctx, projectID)
	_, err = m.save(ctx, init, project)
	return err
}

// Retry re-runs a blocked initialization, or an analysis whose verification
// never completed, with a fresh stable execution ID. The managed Meta-Repo is
// only created when absent, so retry never duplicates it. When executable
// verification commands already exist only verification is repeated; otherwise
// the analysis is re-run so the agent can use the updated configuration.
func (m *Manager) Retry(ctx context.Context, projectID string) error {
	init, err := m.Store.Initialization(ctx, InitID(projectID))
	if err != nil {
		return err
	}
	retryable := init.Status == domain.InitBlocked || (init.Status == domain.InitReady && !init.ReadyForTasks)
	if !retryable {
		return errors.New("当前初始化状态不可重试")
	}
	if m.Agents != nil || m.CancelAnalysis != nil {
		if err = m.cancelAnalysis(ctx, init); err != nil {
			return err
		}
	}
	init.Status = domain.InitPending
	init.Error = ""
	init.Attempts++
	init.ExecutionID = ""
	init.Questions = nil
	init.AgentStopped = false
	switch {
	case !init.RepositoriesConnected:
		// The connect step never completed, so there are no pinned revisions.
		// Resume there instead of starting an analysis that must fail again.
		init.Stage = "connect"
		init.Message = "准备重新连接仓库"
	case init.AnalysisComplete && init.MetaSHA != "":
		init.Stage = "verify"
		init.Message = "准备完成 Meta 仓库初始化（仅静态分析）"
	default:
		init.Stage = "analyze"
		init.Message = "准备重新分析"
	}
	project, _ := m.Store.Project(ctx, projectID)
	_, err = m.save(ctx, init, project)
	return err
}

func executableVerification(verification []domain.InitVerification) bool {
	for _, item := range verification {
		if !item.DryRun && len(item.Command) > 0 {
			return true
		}
	}
	return false
}

// ReadyForTasks reports the persisted Meta initialization readiness used to gate
// enabling a managed project and accepting new tasks for it.
func (m *Manager) ReadyForTasks(ctx context.Context, projectID string) bool {
	init, err := m.Store.Initialization(ctx, InitID(projectID))
	if err != nil {
		return false
	}
	return init.Status == domain.InitReady && init.ReadyForTasks
}

// Answer records missing information and resumes the state machine. The session
// is retained so the analysis continues from its previous context.
func (m *Manager) Answer(ctx context.Context, projectID string, answers map[string]string) (domain.Initialization, error) {
	init, err := m.Store.Initialization(ctx, InitID(projectID))
	if err != nil {
		return init, err
	}
	if init.Status != domain.InitNeedsInput {
		return init, errors.New("当前初始化没有待补充的问题")
	}
	remaining := 0
	for i := range init.Questions {
		if text, ok := answers[init.Questions[i].ID]; ok && strings.TrimSpace(text) != "" {
			init.Questions[i].Answer = strings.TrimSpace(text)
			init.Questions[i].Answered = true
		}
		if !init.Questions[i].Answered {
			remaining++
		}
	}
	if remaining > 0 {
		return init, errors.New("还有未回答的问题")
	}
	init.Status = domain.InitPending
	init.Attempts++
	init.ExecutionID = ""
	init.Message = "已保存补充信息，继续初始化"
	project, err := m.Store.Project(ctx, projectID)
	if err != nil {
		return init, err
	}
	return m.save(ctx, init, project)
}

// Advance runs one step of the state machine. It is safe to call repeatedly and
// after a process restart because every side effect is keyed by stable IDs.
func (m *Manager) Advance(ctx context.Context, init domain.Initialization) (domain.Initialization, error) {
	if init.Status == domain.InitReady && init.Publication != nil && init.Publication.Status == "pending" {
		return m.publishMeta(ctx, init)
	}
	if domain.InitTerminal(init.Status) {
		return init, nil
	}
	project, err := m.Store.Project(ctx, init.ProjectID)
	if err != nil {
		return init, err
	}
	if !project.ManagedMeta() {
		return init, errors.New("initialization requires a managed Meta-Repo project")
	}
	if init.ID == "" {
		init.ID = InitID(init.ProjectID)
	}
	if init.RepositorySHAs == nil {
		init.RepositorySHAs = map[string]string{}
	}
	if init.Stage == "" {
		init.Stage = "connect"
	}
	if init.Status == domain.InitPending || init.Status == "" {
		init.Status = domain.InitRunning
	}
	switch init.Stage {
	case "connect":
		return m.connect(ctx, init, project)
	case "analyze":
		return m.analyze(ctx, init, project)
	case "verify":
		return m.verify(ctx, init, project)
	default:
		init.Status = domain.InitBlocked
		init.Error = "未知的初始化阶段：" + init.Stage
		init.Message = init.Error
		return m.save(ctx, init, project)
	}
}

func (m *Manager) save(ctx context.Context, init domain.Initialization, project domain.Project) (domain.Initialization, error) {
	if err := ctx.Err(); err != nil {
		return init, err
	}
	if g := guardFrom(ctx); g != nil && !g.held() {
		return init, errLeaseLost
	}
	init.UpdatedAt = time.Now().UTC()
	init.Version++
	if err := m.Store.SaveInitialization(ctx, init); err != nil {
		return init, err
	}
	m.syncProjectStatus(ctx, project, init.Status)
	return init, nil
}

// syncProjectStatus mirrors the funnel onto the project for list views. It is
// best-effort: a concurrent console edit must not fail the initialization.
func (m *Manager) syncProjectStatus(ctx context.Context, project domain.Project, status string) {
	if project.ID == "" || project.InitStatus == status {
		return
	}
	current, err := m.Store.Project(ctx, project.ID)
	if err != nil || current.InitStatus == status {
		return
	}
	current.InitStatus = status
	if _, err = m.Store.SaveProject(ctx, current); err != nil {
		m.log("update project init status", err)
	}
}

func addQuestion(questions []domain.InitQuestion, text string) []domain.InitQuestion {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "请补充初始化所需的信息。"
	}
	for i := range questions {
		if questions[i].Question == text {
			questions[i].Answered = false
			questions[i].Answer = ""
			return questions
		}
	}
	return append(questions, domain.InitQuestion{ID: "q" + strconv.Itoa(len(questions)+1), Question: text})
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func (m *Manager) cancelAnalysis(ctx context.Context, in domain.Initialization) error {
	if in.NodeID != "" {
		if m.CancelAnalysis == nil {
			return errors.New("远程初始化取消接口未配置")
		}
		return m.CancelAnalysis(ctx, in)
	}
	if m.Agents != nil {
		return m.Agents.CancelTask(ctx, in.ProjectID)
	}
	return nil
}
