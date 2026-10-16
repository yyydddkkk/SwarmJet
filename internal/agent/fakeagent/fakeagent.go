// Package fakeagent provides a scripted, deterministic execution.AgentBackend
// for tests. It launches no containers, makes no network calls and never
// silently succeeds: every Start consumes a result that the test preset for the
// requested role, and a missing script is reported as an explicit error.
//
// The platform's workflow logic (phase advancement, rework, retry, pause and
// cancel) can therefore be exercised end to end without a real model.
package fakeagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/execution"
)

// ErrNoScript reports a Start for a role with no remaining scripted step.
var ErrNoScript = errors.New("fakeagent: no scripted result for role")

// ErrMissingResult reports a scripted success without a result payload. A
// production execution that succeeds always carries a validated result, so the
// fake refuses to invent one.
var ErrMissingResult = errors.New("fakeagent: scripted success requires a result")

// ErrUnknownExecution reports an Inspect for an execution the fake never saw.
var ErrUnknownExecution = errors.New("fakeagent: unknown execution")

// Execution statuses. They mirror the strings the pipeline interprets.
const (
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusRunning   = "running"
	StatusStarting  = "starting"
)

// Failure codes shared with the production backend.
const (
	FailureTimeout       = "timeout"
	FailureInvalidResult = "invalid_result"
)

// WildcardRole scripts every role that has no explicit step left.
const WildcardRole = "*"

// Step is one scripted agent execution.
type Step struct {
	// Status is the terminal execution status. Empty means StatusSucceeded.
	Status string
	// Running keeps the execution live so a test can interrupt it with pause or
	// cancel. Inspect keeps returning StatusRunning until Cancel is called.
	Running bool
	// Result is the validated result returned on success. It is required for a
	// successful step.
	Result *domain.AgentResult
	// FailureReason and FailureCode describe a failed execution.
	FailureReason string
	FailureCode   string
	// ExitCode defaults to 0.
	ExitCode int
	// Err fails Start itself, before an execution record exists. A retry with
	// the same ID consumes the next scripted step, which models a transient
	// infrastructure failure.
	Err error
	// Files are written under the request workspace, modelling worktree edits.
	Files map[string]string
	// Commits records simulated commit subjects for assertions.
	Commits []string
	// Delay blocks Start until it elapses or the context is cancelled.
	Delay time.Duration
	// SessionID overrides the generated session ID.
	SessionID string
}

// Call records one Start invocation.
type Call struct {
	At        time.Time
	Err       error
	Request   domain.AgentRequest
	Step      Step
	Execution domain.Execution
	// Index is the 1-based call number within the requested role.
	Index int
}

type record struct {
	exec  domain.Execution
	task  string
	role  string
	step  Step
	index int
}

// Backend is an in-memory execution.AgentBackend driven by scripts.
type Backend struct {
	mu      sync.Mutex
	scripts map[string][]Step
	used    map[string]int
	calls   []Call
	execs   map[string]*record
	byTask  map[string][]string
	files   map[string]string
}

// New returns an empty backend. Start fails until a script is added.
func New() *Backend {
	return &Backend{
		scripts: map[string][]Step{},
		used:    map[string]int{},
		execs:   map[string]*record{},
		byTask:  map[string][]string{},
		files:   map[string]string{},
	}
}

// Script appends steps for one role. Steps are consumed in order. The wildcard
// role is used only when the exact role has no remaining step.
func (b *Backend) Script(role string, steps ...Step) *Backend {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scripts[role] = append(b.scripts[role], steps...)
	return b
}

// At scripts one step at an explicit 1-based call number for a role. Gaps are
// filled with an explicit script_gap failure so an unscripted call fails loudly
// instead of silently succeeding.
func (b *Backend) At(role string, call int, step Step) *Backend {
	b.mu.Lock()
	defer b.mu.Unlock()
	if call < 1 {
		call = 1
	}
	steps := b.scripts[role]
	for len(steps) < call {
		steps = append(steps, Step{Status: StatusFailed, FailureCode: "script_gap", FailureReason: "no scripted result at this call"})
	}
	steps[call-1] = step
	b.scripts[role] = steps
	return b
}

// Start consumes the next scripted step for the requested role. It adopts an
// existing execution on a retry with the same durable execution ID.
func (b *Backend) Start(ctx context.Context, req domain.AgentRequest) (domain.Execution, error) {
	b.mu.Lock()
	if existing, ok := b.execs[req.ExecutionID]; ok {
		ex := existing.exec
		b.mu.Unlock()
		return ex, nil
	}
	step, index, ok := b.next(req.Role)
	if !ok {
		b.mu.Unlock()
		return domain.Execution{}, fmt.Errorf("%w %q", ErrNoScript, req.Role)
	}
	if step.Err != nil {
		b.calls = append(b.calls, Call{At: time.Now(), Request: req, Step: step, Index: index, Err: step.Err})
		b.mu.Unlock()
		return domain.Execution{}, step.Err
	}
	if !step.Running && effectiveStatus(step) == StatusSucceeded && step.Result == nil {
		b.mu.Unlock()
		return domain.Execution{}, fmt.Errorf("%w role=%q", ErrMissingResult, req.Role)
	}
	status := effectiveStatus(step)
	session := step.SessionID
	if session == "" {
		session = "fake-session-" + req.Role + "-" + strconv.Itoa(index)
	}
	ex := domain.Execution{ID: req.ExecutionID, SessionID: session, Status: status, ExitCode: step.ExitCode, FailureReason: step.FailureReason, FailureCode: step.FailureCode}
	if req.Workspace != "" {
		ex.LogPath = filepath.Join(req.Workspace, ".swarmjet-fake", req.ExecutionID+".log")
	}
	if status == StatusSucceeded {
		ex.Result = cloneResult(step.Result)
	}
	rec := &record{exec: ex, task: req.TaskID, role: req.Role, step: step, index: index}
	b.execs[req.ExecutionID] = rec
	b.byTask[req.TaskID] = append(b.byTask[req.TaskID], req.ExecutionID)
	call := Call{At: time.Now(), Request: req, Step: step, Execution: ex, Index: index}
	b.mu.Unlock()

	if step.Delay > 0 {
		timer := time.NewTimer(step.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			call.Err = ctx.Err()
			_ = b.Cancel(ctx, req.ExecutionID)
			call.Execution, _ = b.Inspect(ctx, req.ExecutionID)
			b.recordCall(call)
			return call.Execution, ctx.Err()
		case <-timer.C:
		}
	}
	if err := b.materialize(req, step); err != nil {
		call.Err = err
		b.recordCall(call)
		return ex, err
	}
	b.recordCall(call)
	return ex, nil
}

// materialize writes a step's files under the request workspace.
func (b *Backend) materialize(req domain.AgentRequest, step Step) error {
	if len(step.Files) == 0 {
		return nil
	}
	if req.Workspace == "" || req.ReadOnlyWorkspace {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for name, content := range step.Files {
		clean := filepath.Clean(name)
		if clean == "." || filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
			return fmt.Errorf("fakeagent: unsafe workspace file %q", name)
		}
		target := filepath.Join(req.Workspace, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return err
		}
		b.files[target] = content
	}
	return nil
}

func (b *Backend) recordCall(call Call) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, call)
}

// next consumes the next step for role. The caller holds b.mu.
func (b *Backend) next(role string) (Step, int, bool) {
	if steps := b.scripts[role]; b.used[role] < len(steps) {
		index := b.used[role] + 1
		step := steps[b.used[role]]
		b.used[role] = index
		return step, index, true
	}
	if role != WildcardRole {
		if steps := b.scripts[WildcardRole]; b.used[WildcardRole] < len(steps) {
			index := b.used[WildcardRole] + 1
			step := steps[b.used[WildcardRole]]
			b.used[WildcardRole] = index
			return step, index, true
		}
	}
	return Step{}, 0, false
}

// Inspect returns the recorded execution state.
func (b *Backend) Inspect(_ context.Context, id string) (domain.Execution, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec, ok := b.execs[id]
	if !ok {
		return domain.Execution{}, fmt.Errorf("%w %q", ErrUnknownExecution, id)
	}
	return rec.exec, nil
}

// Cancel stops one execution. Cancelling a finished execution is a no-op.
func (b *Backend) Cancel(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	rec, ok := b.execs[id]
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownExecution, id)
	}
	if rec.exec.Status == StatusRunning || rec.exec.Status == StatusStarting {
		rec.exec.Status = StatusCancelled
	}
	return nil
}

// CancelTask stops every live execution belonging to the task.
func (b *Backend) CancelTask(_ context.Context, taskID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range b.byTask[taskID] {
		if rec, ok := b.execs[id]; ok && (rec.exec.Status == StatusRunning || rec.exec.Status == StatusStarting) {
			rec.exec.Status = StatusCancelled
		}
	}
	return nil
}

// TaskExecutions returns the recorded executions for one task in call order.
func (b *Backend) TaskExecutions(_ context.Context, taskID string) ([]domain.Execution, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]domain.Execution, 0, len(b.byTask[taskID]))
	for _, id := range b.byTask[taskID] {
		if rec, ok := b.execs[id]; ok {
			out = append(out, rec.exec)
		}
	}
	return out, nil
}

// Calls returns a snapshot of every Start invocation, including failed ones.
func (b *Backend) Calls() []Call {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Call, len(b.calls))
	copy(out, b.calls)
	return out
}

// CallsForRole returns a snapshot of the calls for one role.
func (b *Backend) CallsForRole(role string) []Call {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Call{}
	for _, call := range b.calls {
		if call.Request.Role == role {
			out = append(out, call)
		}
	}
	return out
}

// Roles returns the role of every call in order.
func (b *Backend) Roles() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.calls))
	for _, call := range b.calls {
		out = append(out, call.Request.Role)
	}
	return out
}

// Files returns the workspace files written so far, keyed by absolute path.
func (b *Backend) Files() map[string]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]string, len(b.files))
	for k, v := range b.files {
		out[k] = v
	}
	return out
}

// Commits returns the simulated commit subjects recorded for one role.
func (b *Backend) Commits(role string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []string{}
	for _, call := range b.calls {
		if call.Request.Role == role {
			out = append(out, call.Step.Commits...)
		}
	}
	return out
}

func effectiveStatus(step Step) string {
	if step.Running {
		return StatusRunning
	}
	if step.Status == "" {
		return StatusSucceeded
	}
	return step.Status
}

func cloneResult(in *domain.AgentResult) *domain.AgentResult {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

// ---------------------------------------------------------------------------
// Step constructors. They keep scenario tests readable and explicit.
// ---------------------------------------------------------------------------

// Succeeded scripts a successful execution with the given result.
func Succeeded(result domain.AgentResult) Step { return Step{Result: &result} }

// Changed scripts a successful development outcome with changes.
func Changed(summary string) Step {
	return Succeeded(domain.AgentResult{Outcome: "changed", Summary: summary})
}

// NoChange scripts a successful development outcome without changes.
func NoChange(summary string) Step {
	return Succeeded(domain.AgentResult{Outcome: "no_change", Summary: summary})
}

// Blocked scripts a successful execution that reports a concrete obstacle.
func Blocked(needsHelp string) Step {
	return Succeeded(domain.AgentResult{Outcome: "blocked", NeedsHelp: needsHelp})
}

// ReviewApproved scripts an approving independent review.
func ReviewApproved(summary string) Step {
	return Succeeded(domain.AgentResult{Outcome: "approved", Summary: summary})
}

// ReviewChangesRequested scripts a review that asks for one rework.
func ReviewChangesRequested(summary string, findings ...domain.Finding) Step {
	return Succeeded(domain.AgentResult{Outcome: "changes_requested", Summary: summary, Findings: findings})
}

// Merged scripts a successful merge execution.
func Merged(summary string) Step {
	return Succeeded(domain.AgentResult{Outcome: "merged", Summary: summary})
}

// Failed scripts a terminal execution failure.
func Failed(reason string) Step {
	return Step{Status: StatusFailed, FailureReason: reason}
}

// TimedOut scripts a terminal execution failure caused by a timeout.
func TimedOut() Step {
	return Step{Status: StatusFailed, FailureCode: FailureTimeout, FailureReason: "agent execution timed out"}
}

// InvalidResult scripts a CLI process that exited zero without a valid result.
// The pipeline answers it with one isolated result-recovery execution.
func InvalidResult() Step {
	return Step{Status: StatusFailed, ExitCode: 0, FailureCode: FailureInvalidResult, FailureReason: "invalid result.json: malformed payload"}
}

// Running scripts a live execution that stays running until it is cancelled.
func Running() Step { return Step{Running: true} }

// Transient scripts an infrastructure error raised by Start itself.
func Transient(err error) Step { return Step{Err: err} }

// WithFiles returns step with workspace files attached.
func WithFiles(step Step, files map[string]string) Step {
	step.Files = files
	return step
}

// WithCommits returns step with simulated commit subjects attached.
func WithCommits(step Step, commits ...string) Step {
	step.Commits = commits
	return step
}

// WithDelay returns step with a blocking delay attached.
func WithDelay(step Step, d time.Duration) Step {
	step.Delay = d
	return step
}

var _ execution.AgentBackend = (*Backend)(nil)
