package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"github.com/yyydddkkk/SwarmJet/internal/artifacts"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/k8srunner"
	"github.com/yyydddkkk/SwarmJet/internal/observability"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// KubernetesWorkflowName is a separate history type while the first offline
// vertical slice is validated. Existing WorkTask histories retain their shape.
const KubernetesWorkflowName = "KubernetesTaskV1"
const KubernetesQueue = "swarmjet-kubernetes"

type KubernetesRunner interface {
	EnsurePVC(context.Context, string, int64) (string, error)
	Create(context.Context, k8srunner.Spec) (k8srunner.Job, error)
	Observe(context.Context, string, int64) (k8srunner.Observation, error)
	ReadPodLog(context.Context, string, int64) ([]byte, error)
	Cancel(context.Context, string, int64) error
}

type KubernetesActivities struct {
	Store     *store.Store
	Runner    KubernetesRunner
	Artifacts artifacts.Store
	Metrics   *observability.Metrics
}

// KubernetesTask runs one deterministic, network isolated Agent contract.
// A stopped or ambiguous attempt is blocked for inspection; this workflow
// never silently creates a replacement Pod.
func KubernetesTask(ctx workflow.Context, task domain.Task) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 3 * time.Hour, HeartbeatTimeout: time.Minute,
		WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	commands := workflow.GetSignalChannel(ctx, SignalName)
	_ = workflow.SetQueryHandler(ctx, "state", func() (domain.Task, error) { return task, nil })
	var cancelRun workflow.CancelFunc
	cancelled, retry := false, false
	seen := map[string]bool{}
	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			var command domain.Command
			commands.Receive(ctx, &command)
			if command.ID != "" {
				if seen[command.ID] {
					continue
				}
				seen[command.ID] = true
			}
			switch command.Kind {
			case "cancel":
				cancelled = true
				if cancelRun != nil {
					cancelRun()
				}
			case "retry":
				retry = true
			case "note":
				task.Notes = append(task.Notes, command.Text)
			}
		}
	})
	persist := func() error {
		return workflow.ExecuteActivity(ctx, "SaveTask", task).Get(ctx, nil)
	}
	for {
		if cancelled {
			if err := workflow.ExecuteActivity(ctx, "StopKubernetesTask", task.ID).Get(ctx, nil); err != nil {
				task.Stage, task.Message = "blocked", "Kubernetes Runner 停止未确认："+err.Error()
				cancelled = false
				if err := persist(); err != nil {
					return err
				}
				continue
			}
			task.Stage, task.Message = "cancelled", "任务已取消；现场保留待检查"
			return persist()
		}
		if task.Stage == "blocked" {
			if err := persist(); err != nil {
				return err
			}
			if err := workflow.Await(ctx, func() bool { return retry || cancelled }); err != nil {
				return err
			}
			if retry {
				retry = false
				task.Stage = "running"
			}
			continue
		}
		task.Stage, task.Message = "running", "Kubernetes Runner 正在执行离线验证"
		if err := persist(); err != nil {
			return err
		}
		if cancelled {
			continue
		}
		runCtx, stop := workflow.WithCancel(ctx)
		cancelRun = stop
		var updated domain.Task
		err := workflow.ExecuteActivity(runCtx, "RunKubernetesTask", task).Get(runCtx, &updated)
		cancelRun = nil
		if cancelled {
			continue
		}
		if err != nil {
			if stopErr := workflow.ExecuteActivity(ctx, "StopKubernetesTask", task.ID).Get(ctx, nil); stopErr != nil {
				task.Message = "执行失败且 Runner 停止未确认：" + stopErr.Error()
			} else {
				task.Message = "执行现场已保留，等待检查：" + err.Error()
			}
			task.Stage = "blocked"
			continue
		}
		task = updated
		task.Stage, task.Message = "completed", "离线 Runner 结果及报告已保存"
		return persist()
	}
}

var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type deterministicResult struct {
	SchemaVersion int    `json:"schemaVersion"`
	ExecutionID   string `json:"executionId"`
	Generation    int64  `json:"generation"`
	TaskID        string `json:"taskId"`
	Status        string `json:"status"`
	InputSHA256   string `json:"inputSha256"`
}

func parseDeterministicResult(data []byte, taskID, executionID string, generation int64) (deterministicResult, error) {
	var result deterministicResult
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) == 0 {
		return result, errors.New("Runner output is empty")
	}
	dec := json.NewDecoder(bytes.NewReader(lines[len(lines)-1]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		return result, fmt.Errorf("invalid Runner result: %w", err)
	}
	if result.SchemaVersion != 1 || result.TaskID != taskID || result.ExecutionID != executionID || result.Generation != generation || result.Status != "succeeded" || !hexDigest.MatchString(result.InputSHA256) {
		return result, errors.New("Runner result identity or digest mismatch")
	}
	return result, nil
}

func (a *KubernetesActivities) RunKubernetesTask(ctx context.Context, task domain.Task) (domain.Task, error) {
	start := time.Now()
	out, err := a.runKubernetesTask(ctx, task)
	a.observeActivity("run_kubernetes_task", task.ID, time.Since(start), err)
	return out, err
}

func (a *KubernetesActivities) runKubernetesTask(ctx context.Context, task domain.Task) (domain.Task, error) {
	if a.Store == nil || a.Runner == nil || a.Artifacts == nil || task.AdmissionBackend != "kubernetes" {
		return task, errors.New("Kubernetes execution is not configured")
	}
	if task.RunnerImage == "" {
		return task, errors.New("Runner image digest is missing")
	}
	executionID := task.ID + "-dev"
	if len(executionID) > 40 {
		return task, errors.New("task ID is too long for Runner Job")
	}
	owned, err := a.Store.BeginKubernetesExecution(ctx, task.ID, "dev", executionID)
	if err != nil {
		return task, err
	}
	if owned.State == "completed" {
		return a.Store.Task(ctx, task.ID)
	}
	pvc, err := a.Runner.EnsurePVC(ctx, task.ID, int64(task.RunnerResources.StorageMi))
	if err != nil {
		return task, err
	}
	var version int64
	if owned.State == "pending" {
		version, err = a.Store.AcquireKubernetesExecution(ctx, executionID, k8srunner.JobName(executionID, 1))
		if err != nil {
			return task, err
		}
	} else if owned.State == "active" && owned.AuthorityVersion == 1 && owned.AttemptID == k8srunner.JobName(executionID, 1) {
		version = owned.AuthorityVersion
	} else {
		return task, fmt.Errorf("execution %s requires manual recovery from %s", executionID, owned.State)
	}
	resource := task.RunnerResources
	spec := k8srunner.Spec{ExecutionID: executionID, Generation: version, TaskID: task.ID,
		Image: task.RunnerImage, PVC: pvc, Command: []string{"/runner"},
		Resources: k8srunner.Resources{CPURequest: strconv.Itoa(resource.CPUMilli) + "m", CPULimit: strconv.Itoa(resource.CPUMilli) + "m", MemoryRequest: strconv.Itoa(resource.MemoryMi) + "Mi", MemoryLimit: strconv.Itoa(resource.MemoryMi) + "Mi"},
	}
	attemptID := k8srunner.JobName(executionID, version)
	if owned.JobCreation != "created" {
		if err := a.Store.MarkKubernetesJobCreating(ctx, executionID, version, attemptID); err != nil {
			return task, err
		}
		err = a.Store.WithKubernetesAuthority(ctx, task.ID, executionID, version, attemptID, func(ctx context.Context) error {
			_, createErr := a.Runner.Create(ctx, spec)
			return createErr
		})
		if err != nil {
			return task, err
		}
		if err := a.Store.MarkKubernetesJobCreated(ctx, executionID, version, attemptID); err != nil {
			return task, err
		}
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	deadline := time.NewTimer(time.Duration(resource.TimeoutSeconds) * time.Second)
	defer deadline.Stop()
	for {
		observation, err := a.Runner.Observe(ctx, executionID, version)
		if err != nil {
			return task, err
		}
		if observation.Failure || observation.Failed > 0 {
			return task, fmt.Errorf("Runner Job failed: %s", observation.Reason)
		}
		if observation.Complete {
			if len(observation.Pods) != 1 || observation.Pods[0].UID == "" || observation.Pods[0].Phase != "Succeeded" {
				return task, errors.New("Job completed without one identified successful Pod")
			}
			break
		}
		if observation.Stopped {
			return task, errors.New("Runner Job disappeared before a validated result")
		}
		activity.RecordHeartbeat(ctx, executionID)
		select {
		case <-ctx.Done():
			return task, ctx.Err()
		case <-deadline.C:
			return task, errors.New("Runner exceeded task timeout")
		case <-tick.C:
		}
	}
	log, err := a.Runner.ReadPodLog(ctx, executionID, version)
	if err != nil {
		return task, err
	}
	parsed, err := parseDeterministicResult(log, task.ID, executionID, version)
	if err != nil {
		return task, err
	}
	current, err := a.Store.CheckKubernetesAuthority(ctx, executionID, version, attemptID)
	if err != nil {
		return task, err
	}
	if !current {
		return task, store.ErrExecutionAuthority
	}
	result := domain.AgentResult{Outcome: "no_change", Summary: "确定性 Runner 完成；输入 SHA-256: " + parsed.InputSHA256}
	resultJSON, _ := json.Marshal(result)
	name := func(kind string) string { return kind + "-g" + strconv.FormatInt(version, 10) }
	resultRef, err := a.Artifacts.Put(ctx, artifacts.Request{TaskID: task.ID, ExecutionID: executionID, Kind: artifacts.Result, Name: name("result"), Role: "dev"}, bytes.NewReader(resultJSON))
	if err != nil {
		return task, err
	}
	logRef, err := a.Artifacts.Put(ctx, artifacts.Request{TaskID: task.ID, ExecutionID: executionID, Kind: artifacts.Log, Name: name("stdout")}, bytes.NewReader(log))
	if err != nil {
		return task, err
	}
	reportRef, err := a.Artifacts.Put(ctx, artifacts.Request{TaskID: task.ID, ExecutionID: executionID, Kind: artifacts.Report, Name: name("deterministic")}, bytes.NewReader(log))
	if err != nil {
		return task, err
	}
	refs := []domain.ArtifactRef{}
	for _, r := range []artifacts.Ref{resultRef, logRef, reportRef} {
		refs = append(refs, domain.ArtifactRef{TaskID: r.TaskID, ExecutionID: r.ExecutionID, Kind: string(r.Kind), Name: r.Name, SHA256: r.SHA256, Size: r.Size})
	}
	if err := a.Store.SaveKubernetesExecutionResult(ctx, executionID, version, attemptID, domain.Execution{ID: executionID, Status: "succeeded", Result: &result, Artifacts: refs}); err != nil {
		return task, err
	}
	return a.Store.Task(ctx, task.ID)
}

// StopKubernetesTask revokes the database right before stopping the Job. It
// returns only after the controller observes both Job and Pods absent.
func (a *KubernetesActivities) StopKubernetesTask(ctx context.Context, taskID string) error {
	start := time.Now()
	err := a.stopKubernetesTask(ctx, taskID)
	a.observeActivity("stop_kubernetes_task", taskID, time.Since(start), err)
	return err
}

func (a *KubernetesActivities) observeActivity(phase, taskID string, elapsed time.Duration, err error) {
	if a.Metrics != nil {
		a.Metrics.Activity(phase, elapsed, err)
	}
	result := "ok"
	if err != nil {
		result = "error"
	}
	slog.Info("task activity step finished", "task_id", taskID, "phase", phase, "result", result, "duration_ms", elapsed.Milliseconds())
}

func (a *KubernetesActivities) stopKubernetesTask(ctx context.Context, taskID string) error {
	if a.Store == nil || a.Runner == nil {
		return errors.New("Kubernetes execution is not configured")
	}
	// Freeze admission first. Task.ExecutionID is a projection and may still be
	// empty while BeginKubernetesExecution is committing; the admission row
	// serializes those operations and prevents a later Acquire.
	ex, err := a.Store.FreezeKubernetesAdmission(ctx, taskID)
	if err != nil {
		return err
	}
	if ex.ExecutionID == "" {
		return nil
	}
	for {
		ex, err = a.Store.KubernetesExecution(ctx, ex.ExecutionID)
		if err != nil {
			return err
		}
		if ex.State != "pending" {
			break
		}
		if err = a.Store.CancelPendingKubernetesExecution(ctx, ex.ExecutionID); err == nil {
			return nil
		}
		if !errors.Is(err, store.ErrExecutionAuthority) {
			return err
		}
	}
	if ex.State == "completed" || ex.State == "cancelled" || ex.State == "stopped" {
		return nil
	}
	if ex.State == "active" {
		if err := a.Store.RevokeKubernetesExecution(ctx, ex.ExecutionID, ex.AuthorityVersion, ex.AttemptID); err != nil {
			return err
		}
		// Re-read after revocation: a creator may have changed JobCreation
		// between the first read and the compare-and-swap above.
		ex, err = a.Store.KubernetesExecution(ctx, ex.ExecutionID)
		if err != nil {
			return err
		}
	}
	if ex.JobCreation == "creating" {
		// A create request may still land after an empty API observation. Best
		// effort deletion preserves evidence but cannot authorize cleanup.
		_ = a.Runner.Cancel(ctx, ex.ExecutionID, ex.AuthorityVersion)
		return errors.New("Job creation response is ambiguous; manual Kubernetes reconciliation required")
	}
	if err := a.Runner.Cancel(ctx, ex.ExecutionID, ex.AuthorityVersion); err != nil {
		return err
	}
	return a.Store.ConfirmKubernetesPodStopped(ctx, ex.ExecutionID, ex.AuthorityVersion, ex.AttemptID, false)
}
