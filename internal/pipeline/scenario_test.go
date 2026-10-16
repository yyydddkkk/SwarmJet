package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/yyydddkkk/SwarmJet/internal/agent/fakeagent"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/execution"
	"github.com/yyydddkkk/SwarmJet/internal/forge"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// scenario_test.go drives the real WorkTask workflow and the real Activities
// against a scripted AgentBackend (internal/agent/fakeagent), a fake execution
// node and a fake hosting client. Only the store is real: it uses the
// TEST_DATABASE_URL isolation helper, so the determinism comes from the Agent
// script and the database, not from a mocked workflow step.

const scenarioNodeID = "scenario-node"

func scenarioAgentConfig() domain.AgentConfig {
	return domain.AgentConfig{Adapter: "opencode", Model: "provider/model", Image: "agent:test", TimeoutSeconds: 300}
}

func scenarioProject() domain.Project {
	return domain.Project{
		ID: "scenario", Name: "Scenario", Enabled: true,
		MetaMode:         domain.MetaModeDirect,
		GitLabURL:        "https://gitlab.test",
		CredentialRef:    "GITLAB_TOKEN",
		AcceptanceStrategy: "repository",
		MaxReworks:       3,
		Development:      scenarioAgentConfig(),
		Review:           scenarioAgentConfig(),
		Repositories: []domain.Repository{{
			ID: "svc", ProjectID: 10, URL: "https://gitlab.test/group/svc.git", Path: ".", TargetBranch: "main",
		}},
	}
}

func scenarioRevision() domain.Revision {
	return domain.Revision{
		RepositoryID: "svc", ProjectID: 10, BaseSHA: "base", HeadSHA: "head",
		Branch: "swarmjet/scenario-task", TargetBranch: "main", Changed: true,
	}
}

func scenarioTask() domain.Task {
	return domain.Task{
		ID: "scenario-task", NodeID: scenarioNodeID, Stage: "queued",
		Project: scenarioProject(),
		Issue: domain.Issue{
			Provider: domain.ProviderGitLab, ProjectID: 10, IID: 7, Title: "Improve service",
			URL: "https://gitlab.test/group/svc/-/issues/7", State: "opened", Labels: []string{"autonomous"},
		},
		Workspace:  domain.Workspace{TaskID: "scenario-task", Revisions: []domain.Revision{scenarioRevision()}},
		Executions: map[string]domain.Execution{},
	}
}

// scenarioDevResult is a complete development handoff: the platform publishes
// and verifies exactly what the agent reports.
func scenarioDevResult(summary string) domain.AgentResult {
	return domain.AgentResult{
		Outcome: "changed", Summary: summary,
		AcceptanceCommands: []domain.AcceptanceCommand{{
			Name: "unit tests", Path: ".", Argv: []string{"go", "test", "./..."},
			Evidence: "AGENTS.md requires go test for the service repository",
		}},
		HumanAcceptance: &domain.ManualAcceptance{
			Prerequisites: []string{"Go toolchain and a local database"},
			Steps:         []domain.ManualStep{{Action: "start the service", Expected: "health endpoint returns ok"}},
			Notes:         []string{"Covered by automated tests except the health endpoint"},
		},
	}
}

func scenarioDevStep(summary string) fakeagent.Step {
	return fakeagent.WithCommits(
		fakeagent.WithFiles(
			fakeagent.Succeeded(scenarioDevResult(summary)),
			map[string]string{"service.go": "package main // " + summary + "\n"},
		),
		"feat: "+summary,
	)
}

// scenarioNode is a fully in-memory execution.Node. Agent lifecycle calls go to
// the scripted backend; workspace and integration calls are deterministic.
type scenarioNode struct {
	backend       *fakeagent.Backend
	root          string
	revisions     []domain.Revision
	verifications []domain.Verification

	mu          sync.Mutex
	beforeStart func(role string, call int)
}

func newScenarioNode(t *testing.T, backend *fakeagent.Backend, revisions []domain.Revision) *scenarioNode {
	t.Helper()
	return &scenarioNode{backend: backend, root: t.TempDir(), revisions: revisions}
}

func (n *scenarioNode) ID() string { return scenarioNodeID }

func (n *scenarioNode) Start(ctx context.Context, req domain.AgentRequest) (domain.Execution, error) {
	if req.Workspace == "" {
		req.Workspace = n.root
	}
	n.mu.Lock()
	hook := n.beforeStart
	n.mu.Unlock()
	if hook != nil {
		hook(req.Role, len(n.backend.CallsForRole(req.Role))+1)
	}
	return n.backend.Start(ctx, req)
}

func (n *scenarioNode) Inspect(ctx context.Context, id string) (domain.Execution, error) {
	return n.backend.Inspect(ctx, id)
}

func (n *scenarioNode) Cancel(ctx context.Context, id string) error {
	return n.backend.Cancel(ctx, id)
}

func (n *scenarioNode) CancelTask(ctx context.Context, taskID string) error {
	return n.backend.CancelTask(ctx, taskID)
}

func (n *scenarioNode) TaskExecutions(ctx context.Context, taskID string) ([]domain.Execution, error) {
	return n.backend.TaskExecutions(ctx, taskID)
}

func (n *scenarioNode) SyncManagedMeta(context.Context, string, string, []byte) error { return nil }

func (n *scenarioNode) PrepareWorkspace(_ context.Context, taskID string, _ domain.Project, _ map[string]string) (domain.Workspace, error) {
	return domain.Workspace{TaskID: taskID, Root: n.root, Revisions: append([]domain.Revision(nil), n.revisions...)}, nil
}

func (n *scenarioNode) InspectWorkspace(_ context.Context, ws domain.Workspace, _ domain.Project, _ map[string]string) (domain.Workspace, error) {
	return ws, nil
}

func (n *scenarioNode) RefreshTargets(_ context.Context, ws domain.Workspace, _ domain.Project, _ map[string]string) (domain.Workspace, error) {
	return ws, nil
}

func (n *scenarioNode) ReleaseWorkspace(context.Context, domain.Workspace) error { return nil }

func (n *scenarioNode) StartIntegration(context.Context, domain.Workspace, domain.IntegrationConfig, map[string]string) error {
	return nil
}

func (n *scenarioNode) VerifyIntegration(context.Context, domain.Workspace, domain.IntegrationConfig, map[string]string) ([]domain.Verification, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]domain.Verification(nil), n.verifications...), nil
}

func (n *scenarioNode) StopIntegration(context.Context, domain.Workspace, domain.IntegrationConfig, map[string]string) error {
	return nil
}

func (n *scenarioNode) ReadReport(context.Context, string, string) ([]byte, error) { return nil, nil }

func (n *scenarioNode) Cleanup(context.Context, string, domain.Workspace, domain.IntegrationConfig, map[string]string) (bool, error) {
	return true, nil
}

func (n *scenarioNode) Retire(context.Context, domain.Task) error { return nil }

func (n *scenarioNode) PushWorkspace(context.Context, domain.Workspace, domain.Project, map[string]string) error {
	return nil
}

// scenarioForge is a GitLab-shaped hosting client. It flips a merge request to
// merged as soon as the merge agent runs, so the platform observes the exact
// state transition a real merge would produce.
type scenarioForge struct {
	forge.Client

	mu           sync.Mutex
	issue        domain.Issue
	mr           forge.MergeRequest
	merged       bool
	descriptions int
	closed       int
}

func newScenarioForge(revision domain.Revision) *scenarioForge {
	mr := forge.MergeRequest{
		IID: 1, URL: "https://gitlab.test/group/svc/-/merge_requests/1",
		SourceBranch: revision.Branch, TargetBranch: revision.TargetBranch,
		SHA: revision.HeadSHA, State: "opened", DetailedMergeStatus: "mergeable",
	}
	return &scenarioForge{
		issue: domain.Issue{Provider: domain.ProviderGitLab, ProjectID: 10, IID: 7, State: "opened", Labels: []string{"autonomous"}},
		mr:    mr,
	}
}

func (f *scenarioForge) markMerged() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merged = true
}

func (f *scenarioForge) GetIssue(_ context.Context, projectID, iid int64) (domain.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	issue := f.issue
	issue.ProjectID, issue.IID = projectID, iid
	return issue, nil
}

func (f *scenarioForge) Dependencies(context.Context, domain.Issue) ([]domain.IssueRef, error) {
	return nil, nil
}

func (f *scenarioForge) EnsureTarget(_ context.Context, repository domain.Repository) (string, error) {
	return repository.TargetBranch, nil
}

func (f *scenarioForge) FindMR(_ context.Context, _ int64, branch, target string) (*forge.MergeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mr.SourceBranch != branch || f.mr.TargetBranch != target {
		return nil, nil
	}
	mr := f.mr
	return &mr, nil
}

func (f *scenarioForge) GetMR(_ context.Context, _ int64, _ int64) (forge.MergeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mr := f.mr
	if f.merged {
		mr.State = "merged"
	}
	return mr, nil
}

func (f *scenarioForge) ClosingIssues(context.Context, int64, int64) ([]domain.IssueRef, error) {
	return nil, nil
}

func (f *scenarioForge) GetBranch(_ context.Context, _ int64, name string) (forge.Branch, error) {
	branch := forge.Branch{Name: name}
	branch.Commit.ID = "base"
	return branch, nil
}

func (f *scenarioForge) RequiredCIPassed(context.Context, int64, int64, string, bool) (bool, error) {
	return true, nil
}

func (f *scenarioForge) UpdateLabels(context.Context, domain.Issue, []string, []string) error {
	return nil
}

func (f *scenarioForge) ListProjectLabels(context.Context, int64) ([]forge.ProjectLabel, error) {
	return nil, nil
}

func (f *scenarioForge) CreateProjectLabel(context.Context, int64, forge.ProjectLabel) error {
	return nil
}

func (f *scenarioForge) UpsertProgress(context.Context, domain.Issue, string, string) error {
	return nil
}

func (f *scenarioForge) UpsertIssueReview(context.Context, domain.Issue, string, string, string) (string, error) {
	return "https://gitlab.test/group/svc/-/issues/7#note_1", nil
}

func (f *scenarioForge) UpdateMRDescription(context.Context, int64, int64, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.descriptions++
	return nil
}

func (f *scenarioForge) CloseIssue(context.Context, domain.Issue) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

type scenarioResolver struct{ node execution.Node }

func (r scenarioResolver) Node(context.Context, string) (execution.Node, error) { return r.node, nil }

type scenarioHarness struct {
	t      *testing.T
	store  *store.Store
	backend *fakeagent.Backend
	node   *scenarioNode
	forge  *scenarioForge
	activities *Activities
	env    *testsuite.TestWorkflowEnvironment

	mu     sync.Mutex
	phases []string
	saved  []domain.Task
}

func newScenarioHarness(t *testing.T, backend *fakeagent.Backend) *scenarioHarness {
	t.Helper()
	ctx := context.Background()
	s := isolatedPipelineStore(t)
	if err := s.HeartbeatNode(ctx, domain.Node{ID: scenarioNodeID, Address: "http://scenario-node", Capacity: 4}); err != nil {
		t.Fatal(err)
	}
	forgeClient := newScenarioForge(scenarioRevision())
	node := newScenarioNode(t, backend, []domain.Revision{scenarioRevision()})
	node.verifications = []domain.Verification{{Name: "unit tests", Required: true, Passed: true}}
	activities := &Activities{
		Store: s, Agents: backend, Nodes: scenarioResolver{node: node}, Concurrency: 2,
		ForgeClient:       func(domain.Project, bool) (forge.Client, error) { return forgeClient, nil },
		ProjectCredential: func(domain.Project, bool) (string, error) { return "scenario-token", nil },
	}
	h := &scenarioHarness{t: t, store: s, backend: backend, node: node, forge: forgeClient, activities: activities}
	h.env = (&testsuite.WorkflowTestSuite{}).NewTestWorkflowEnvironment()
	h.env.SetTestTimeout(90 * time.Second)
	h.env.RegisterActivityWithOptions(func(ctx context.Context, in StepInput) (StepResult, error) {
		h.mu.Lock()
		h.phases = append(h.phases, in.Phase)
		h.mu.Unlock()
		return activities.Step(ctx, in)
	}, activity.RegisterOptions{Name: "Step"})
	h.env.RegisterActivityWithOptions(func(ctx context.Context, task domain.Task) error {
		h.mu.Lock()
		h.saved = append(h.saved, task)
		h.mu.Unlock()
		return activities.SaveTask(ctx, task)
	}, activity.RegisterOptions{Name: "SaveTask"})
	return h
}

func (h *scenarioHarness) persistInitial(task domain.Task) {
	h.t.Helper()
	ctx := context.Background()
	project, err := h.store.SaveProject(ctx, task.Project)
	if err != nil {
		h.t.Fatal(err)
	}
	task.Project = project
	body, err := json.Marshal(task)
	if err != nil {
		h.t.Fatal(err)
	}
	source := domain.IssueSource(project.HostingURL(), task.Issue)
	if _, err = h.store.Pool.Exec(ctx,
		`INSERT INTO tasks(id,project_id,source,stage,node_id,body) VALUES($1,$2,$3,$4,$5,$6)`,
		task.ID, project.ID, source, task.Stage, task.NodeID, body); err != nil {
		h.t.Fatal(err)
	}
}

func (h *scenarioHarness) run(task domain.Task) domain.Task {
	h.t.Helper()
	h.persistInitial(task)
	h.env.ExecuteWorkflow(WorkTask, task)
	if !h.env.IsWorkflowCompleted() {
		h.t.Fatal("workflow did not complete")
	}
	if err := h.env.GetWorkflowError(); err != nil {
		h.t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.saved) == 0 {
		h.t.Fatal("workflow persisted no state")
	}
	return h.saved[len(h.saved)-1]
}

func (h *scenarioHarness) recordedPhases() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.phases...)
}

func (h *scenarioHarness) state() domain.Task {
	h.t.Helper()
	value, err := h.env.QueryWorkflow("state")
	if err != nil {
		h.t.Fatal(err)
	}
	var task domain.Task
	if err = value.Get(&task); err != nil {
		h.t.Fatal(err)
	}
	return task
}

func (h *scenarioHarness) signalAfter(d time.Duration, command domain.Command) {
	h.env.RegisterDelayedCallback(func() { h.env.SignalWorkflow(SignalName, command) }, d)
}

// TestScenarioHappyPath drives development, platform verification, independent
// review and automatic merge through the real activities and the fake agent.
func TestScenarioHappyPath(t *testing.T) {
	backend := fakeagent.New().
		Script("dev", scenarioDevStep("implemented"), fakeagent.Changed("pushed the branch")).
		Script("review", fakeagent.ReviewApproved("the change is correct")).
		At("merge", 1, fakeagent.Merged("merged the MR"))
	h := newScenarioHarness(t, backend)
	h.node.beforeStart = func(role string, _ int) {
		if role == "merge" {
			h.forge.markMerged()
		}
	}
	final := h.run(scenarioTask())
	if final.Stage != "completed" {
		t.Fatalf("stage = %s, message = %q", final.Stage, final.Message)
	}
	if roles := backend.Roles(); len(roles) != 4 || roles[0] != "dev" || roles[1] != "dev" || roles[2] != "review" || roles[3] != "merge" {
		t.Fatalf("agent roles = %v", roles)
	}
	phases := h.recordedPhases()
	want := []string{"waiting_dependencies", "preparing", "developing", "publishing", "verifying", "reviewing", "acquiring_merge", "merging", "finalizing"}
	if !equalStrings(phases, want) {
		t.Fatalf("phases = %v want %v", phases, want)
	}
	if h.forge.closed != 1 || final.ReviewDigest != domain.Digest(final.Workspace.Revisions) {
		t.Fatalf("issue closed = %d, review digest = %q", h.forge.closed, final.ReviewDigest)
	}
	if len(backend.Files()) == 0 || len(backend.Commits("dev")) != 1 {
		t.Fatalf("fake agent did not record its workspace writes: files = %v commits = %v", backend.Files(), backend.Commits("dev"))
	}
	if h.forge.descriptions == 0 {
		t.Fatal("publication never refreshed the merge request description")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func countPhase(phases []string, phase string) int {
	count := 0
	for _, got := range phases {
		if got == phase {
			count++
		}
	}
	return count
}

// TestScenarioReviewReworkRunsDevelopmentAgain makes the review reject the
// first attempt; the platform must rework exactly once and then merge.
func TestScenarioReviewReworkRunsDevelopmentAgain(t *testing.T) {
	backend := fakeagent.New().
		Script("dev",
			scenarioDevStep("first attempt"),
			fakeagent.Changed("first push"),
			scenarioDevStep("second attempt"),
			fakeagent.Changed("second push")).
		Script("review",
			fakeagent.ReviewChangesRequested("input validation is missing", domain.Finding{RepositoryID: "svc", Path: "main.go", Message: "validate input"}),
			fakeagent.ReviewApproved("the revision addresses the finding")).
		Script("merge", fakeagent.Merged("merged after rework"))
	h := newScenarioHarness(t, backend)
	h.node.beforeStart = func(role string, _ int) {
		if role == "merge" {
			h.forge.markMerged()
		}
	}
	final := h.run(scenarioTask())
	if final.Stage != "completed" || final.Reworks != 1 {
		t.Fatalf("stage = %s reworks = %d, message = %q", final.Stage, final.Reworks, final.Message)
	}
	phases := h.recordedPhases()
	if countPhase(phases, "developing") != 2 || countPhase(phases, "reviewing") != 2 || countPhase(phases, "merging") != 1 {
		t.Fatalf("rework phases = %v", phases)
	}
	if roles := backend.Roles(); len(roles) != 7 {
		t.Fatalf("agent calls = %v", roles)
	}
	found := false
	for _, note := range final.Notes {
		found = found || note == "input validation is missing"
	}
	if !found {
		t.Fatalf("review feedback was lost: %v", final.Notes)
	}
}

// TestScenarioAgentFailureThenRetryBlocksAndResumes covers the platform retry
// policy: a failed agent execution blocks the task, and one explicit retry
// resumes the same phase and succeeds.
func TestScenarioAgentFailureThenRetryBlocksAndResumes(t *testing.T) {
	backend := fakeagent.New().
		Script("dev",
			fakeagent.Failed("model process crashed"),
			scenarioDevStep("recovered attempt"),
			fakeagent.Changed("pushed after retry")).
		Script("review", fakeagent.ReviewApproved("approved after retry")).
		Script("merge", fakeagent.Merged("merged after retry"))
	h := newScenarioHarness(t, backend)
	h.node.beforeStart = func(role string, _ int) {
		if role == "merge" {
			h.forge.markMerged()
		}
	}
	blocked := false
	h.signalAfter(time.Second, domain.Command{ID: "retry-agent", Kind: "retry"})
	h.env.RegisterDelayedCallback(func() {
		state := h.state()
		blocked = state.Stage == "blocked" && strings.Contains(state.Message, "model process crashed")
	}, 500*time.Millisecond)
	final := h.run(scenarioTask())
	if !blocked {
		t.Fatalf("agent failure did not block with its reason: stage = %s message = %q", final.Stage, final.Message)
	}
	if final.Stage != "completed" || final.Reworks != 0 {
		t.Fatalf("stage = %s reworks = %d, message = %q", final.Stage, final.Reworks, final.Message)
	}
	if calls := backend.CallsForRole("dev"); len(calls) != 3 || calls[0].Execution.Status != fakeagent.StatusFailed {
		t.Fatalf("dev calls = %+v", calls)
	}
	if countPhase(h.recordedPhases(), "developing") != 2 {
		t.Fatalf("retry did not rerun development: %v", h.recordedPhases())
	}
}

// TestScenarioTransientAgentErrorRetriedByTemporal covers the workflow retry
// policy: Start itself failing is a transient activity error, so Temporal
// retries the activity without a human command and the next attempt succeeds.
func TestScenarioTransientAgentErrorRetriedByTemporal(t *testing.T) {
	backend := fakeagent.New().
		Script("dev",
			fakeagent.Transient(errors.New("execution host unavailable")),
			fakeagent.Transient(errors.New("execution host unavailable")),
			scenarioDevStep("succeeded on the third attempt"),
			fakeagent.Changed("pushed")).
		Script("review", fakeagent.ReviewApproved("approved")).
		Script("merge", fakeagent.Merged("merged"))
	h := newScenarioHarness(t, backend)
	h.node.beforeStart = func(role string, _ int) {
		if role == "merge" {
			h.forge.markMerged()
		}
	}
	final := h.run(scenarioTask())
	if final.Stage != "completed" {
		t.Fatalf("stage = %s message = %q", final.Stage, final.Message)
	}
	if calls := backend.CallsForRole("dev"); len(calls) != 4 {
		t.Fatalf("expected two transient retries plus two dev phases, got %+v", calls)
	}
}

// TestScenarioPauseAndResumeDuringDevelopment interrupts a live agent run with
// pause and then resumes. The platform must cancel the execution, recover it and
// rerun development from a fresh session before completing.
func TestScenarioPauseAndResumeDuringDevelopment(t *testing.T) {
	backend := fakeagent.New().
		Script("dev",
			fakeagent.Running(),
			scenarioDevStep("after resume"),
			fakeagent.Changed("pushed after resume")).
		Script("review", fakeagent.ReviewApproved("approved after resume")).
		Script("merge", fakeagent.Merged("merged after resume"))
	h := newScenarioHarness(t, backend)
	var once sync.Once
	h.node.beforeStart = func(role string, call int) {
		if role == "merge" {
			h.forge.markMerged()
			return
		}
		if role == "dev" && call == 1 {
			once.Do(func() {
				// Delivered from the running activity; order is preserved so the
				// pause cancels the execution before the resume is applied.
				h.env.SignalWorkflow(SignalName, domain.Command{ID: "pause-during-agent", Kind: "pause"})
				h.env.SignalWorkflow(SignalName, domain.Command{ID: "resume-during-agent", Kind: "resume"})
			})
		}
	}
	final := h.run(scenarioTask())
	if final.Stage != "completed" || final.Paused {
		t.Fatalf("stage = %s paused = %v", final.Stage, final.Paused)
	}
	phases := h.recordedPhases()
	if countPhase(phases, "recover") != 1 || countPhase(phases, "developing") != 2 {
		t.Fatalf("pause did not interrupt and rerun development: %v", phases)
	}
	calls := backend.CallsForRole("dev")
	if len(calls) != 3 {
		t.Fatalf("dev calls = %+v", calls)
	}
	for _, call := range calls {
		if call.Request.SessionID == "" && call.Index > 1 {
			t.Fatalf("resumed attempt did not start a fresh session: %+v", call)
		}
	}
}

// TestScenarioInvalidResultRecoveryRunsOneReadOnlyHandoff covers the malformed
// result path: the pipeline gives the agent exactly one fresh, read-only
// result-recovery execution and continues from its handoff.
func TestScenarioInvalidResultRecoveryRunsOneReadOnlyHandoff(t *testing.T) {
	backend := fakeagent.New().
		Script("dev",
			fakeagent.InvalidResult(),
			scenarioDevStep("recovered handoff"),
			fakeagent.Changed("pushed after recovery")).
		Script("review", fakeagent.ReviewApproved("approved")).
		Script("merge", fakeagent.Merged("merged"))
	h := newScenarioHarness(t, backend)
	h.node.beforeStart = func(role string, _ int) {
		if role == "merge" {
			h.forge.markMerged()
		}
	}
	final := h.run(scenarioTask())
	if final.Stage != "completed" {
		t.Fatalf("stage = %s message = %q", final.Stage, final.Message)
	}
	calls := backend.CallsForRole("dev")
	if len(calls) != 3 {
		t.Fatalf("dev calls = %+v", calls)
	}
	recovery := calls[1]
	if !recovery.Request.ReadOnlyWorkspace || !strings.HasSuffix(recovery.Request.ExecutionID, "-result") || recovery.Request.SessionID != "" {
		t.Fatalf("result recovery request = %+v", recovery.Request)
	}
	if recovery.Execution.Status != fakeagent.StatusSucceeded || recovery.Execution.Result == nil || recovery.Execution.Result.Outcome != "changed" {
		t.Fatalf("result recovery execution = %+v", recovery.Execution)
	}
}

// TestScenarioAutoMergeDisabledWaitsForHumanMerge keeps the switch off: the
// workflow must not initiate a merge, must wait for the human merge and only
// then finalize.
func TestScenarioAutoMergeDisabledWaitsForHumanMerge(t *testing.T) {
	backend := fakeagent.New().
		Script("dev", scenarioDevStep("implemented"), fakeagent.Changed("pushed")).
		Script("review", fakeagent.ReviewApproved("approved"))
	h := newScenarioHarness(t, backend)
	h.env.RegisterDelayedCallback(func() { h.forge.markMerged() }, time.Second)
	task := scenarioTask()
	task.Project.AutoMerge = autoMerge(false)
	final := h.run(task)
	if final.Stage != "completed" {
		t.Fatalf("stage = %s message = %q", final.Stage, final.Message)
	}
	phases := h.recordedPhases()
	if countPhase(phases, "waiting_manual_merge") == 0 {
		t.Fatalf("workflow never waited for the human merge: %v", phases)
	}
	if countPhase(phases, "acquiring_merge") != 0 || countPhase(phases, "merging") != 0 {
		t.Fatalf("workflow initiated a merge while auto-merge was off: %v", phases)
	}
	if final.AutoMergeEnabled() {
		t.Fatalf("auto-merge switch was lost: %+v", final)
	}
	if calls := backend.CallsForRole("merge"); len(calls) != 0 {
		t.Fatalf("merge agent ran without automatic merge: %+v", calls)
	}
}
