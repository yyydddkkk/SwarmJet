package pipeline

import (
	"fmt"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// dispatchPhase advances the workflow after one stage Activity completed. The
// phase is the stage that produced the result; handlers may update both the
// task and the phase recorded for the next iteration. It reports loopFinish
// when the run loop must return err, including the successful stop path.
func (w *workTask) dispatchPhase(phase string, result StepResult) (loopAction, error) {
	var err error
	switch phase {
	case phaseStopping:
		return loopFinish, w.stageStopping()
	case domain.StageWaitingDependencies:
		err = w.stageWaitingDependencies(result)
	case domain.StagePreparing:
		err = w.stagePreparing()
	case domain.StageDeveloping:
		err = w.stageDeveloping()
	case domain.StageVerifying:
		err = w.stageVerifying(result)
	case domain.StagePublishing:
		err = w.stagePublishing()
	case domain.StageReviewing:
		err = w.stageReviewing()
	case domain.StageAcquiringMerge:
		err = w.stageAcquiringMerge(result)
	case domain.StageWaitingManualMerge:
		err = w.stageWaitingManualMerge(result)
	case domain.StageMerging, domain.StagePartialMerge:
		err = w.stageMerging(result)
	case domain.StageManualMerging:
		err = w.stageManualMerging(result)
	case domain.StageFinalizing:
		err = w.stageFinalizing()
	default:
		return loopFinish, fmt.Errorf("unknown phase %q", phase)
	}
	if err != nil {
		return loopFinish, err
	}
	return loopNext, nil
}

func (w *workTask) stageStopping() error {
	w.task.Stage = domain.StageCancelled
	w.task.Message = "任务已取消，现场保留"
	return w.persist()
}

func (w *workTask) stageWaitingDependencies(result StepResult) error {
	if result.Ready {
		w.task.Stage = domain.StagePreparing
	} else {
		w.waitTick(120 * time.Second)
	}
	return nil
}

func (w *workTask) stagePreparing() error {
	w.task.Stage = domain.StageDeveloping
	return nil
}

func (w *workTask) stageDeveloping() error {
	ex := w.task.Executions["dev"]
	if ex.Result == nil || ex.Result.Outcome == "blocked" {
		w.task.Stage = domain.StageBlocked
		if ex.Result != nil {
			w.task.Message = ex.Result.NeedsHelp
		}
		return nil
	}
	w.task.Assumptions = append(w.task.Assumptions, ex.Result.Assumptions...)
	if w.skipAcceptance || w.earlyPublish {
		w.task.Stage = domain.StagePublishing
	} else {
		w.task.Stage = domain.StageVerifying
	}
	return nil
}

func (w *workTask) stageVerifying(result StepResult) error {
	if result.Waiting {
		w.ciWaits++
		if w.ciWaits <= 60 {
			w.waitTick(30 * time.Second)
			return nil
		}
		w.task.AcceptanceIncomplete = true
		w.task.Message = "等待 CI 超过 30 分钟，保留 MR 供人工验收，禁止自动合并"
		result.Ready = true
	} else {
		w.ciWaits = 0
	}

	if result.Ready {
		if w.earlyPublish {
			if w.reviewEnabled {
				w.task.Stage = domain.StageReviewing
			} else {
				w.advanceAfterReview()
			}
		} else {
			w.task.Stage = domain.StagePublishing
		}
		return nil
	}
	w.task.Reworks++
	w.task.ApprovalDigest = ""
	w.task.ReviewDigest = ""
	failed := []string{}
	for _, check := range w.task.Verification {
		if check.Required && !check.Passed {
			failed = append(failed, check.Name)
		}
	}
	w.task.Message = "必需验收未通过，请读取报告并修复"
	if len(failed) > 0 {
		w.task.Message += "：" + strings.Join(failed, "、")
	}
	w.task.Notes = append(w.task.Notes, w.task.Message)
	if w.task.Reworks > w.task.Project.MaxReworks {
		w.task.Stage = domain.StageBlocked
		w.previousPhase = domain.StageDeveloping
	} else {
		w.task.Stage = domain.StageDeveloping
	}
	return nil
}

func (w *workTask) stagePublishing() error {
	ex := w.task.Executions["dev"]
	if ex.Result != nil && ex.Result.Outcome == "blocked" {
		w.task.Stage = domain.StageBlocked
		w.task.Message = ex.Result.NeedsHelp
		return nil
	}
	if w.skipAcceptance {
		if w.reviewEnabled {
			w.task.Stage = domain.StageReviewing
		} else {
			w.advanceAfterReview()
		}
	} else if w.earlyPublish {
		w.task.Stage = domain.StageVerifying
	} else {
		if w.reviewEnabled {
			w.task.Stage = domain.StageReviewing
		} else {
			w.advanceAfterReview()
		}
	}
	return nil
}

func (w *workTask) stageReviewing() error {
	ex := w.task.Executions["review"]
	if ex.Result == nil || ex.Result.Outcome == "blocked" {
		w.task.Stage = domain.StageBlocked
		if ex.Result != nil {
			w.task.Message = ex.Result.NeedsHelp
		}
		return nil
	}
	if ex.Result.Outcome != "approved" {
		w.task.Reworks++
		w.task.Notes = append(w.task.Notes, ex.Result.Summary)
		for _, f := range ex.Result.Findings {
			w.task.Notes = append(w.task.Notes, fmt.Sprintf("%s/%s: %s", f.RepositoryID, f.Path, f.Message))
		}
		w.task.ApprovalDigest = ""
		w.task.ReviewDigest = ""
		if w.task.Reworks > w.task.Project.MaxReworks {
			w.task.Stage = domain.StageBlocked
			w.previousPhase = domain.StageDeveloping
		} else {
			w.task.Stage = domain.StageDeveloping
		}
		return nil
	}
	w.advanceAfterReview()
	return nil
}

func (w *workTask) stageAcquiringMerge(result StepResult) error {
	if w.task.Stage == domain.StageWaitingApproval || w.task.Stage == domain.StageWaitingManualMerge {
		return nil
	}
	if result.Ready {
		w.ciWaits = 0
		w.task.Stage = domain.StageMerging
		return nil
	}
	if strings.HasPrefix(w.task.Message, "等待 GitLab") {
		w.ciWaits++
		if w.ciWaits >= 180 {
			w.task.Stage = domain.StageBlocked
			w.task.Message = "GitLab CI 或合并检查等待超过 30 分钟，请检查后重试"
			w.ciWaits = 0
			return nil
		}
	}
	w.waitTick(10 * time.Second)
	return nil
}

func (w *workTask) stageWaitingManualMerge(result StepResult) error {
	if w.task.Stage == domain.StageWaitingApproval {
		return nil
	}
	if w.task.AutoMergeEnabled() && !w.task.AcceptanceIncomplete && !result.Ready {
		w.task.Stage = domain.StageAcquiringMerge
		return nil
	}
	if result.Ready {
		w.task.Stage = domain.StageFinalizing
	} else {
		w.waitTick(30 * time.Second)
	}
	return nil
}

func (w *workTask) stageMerging(result StepResult) error {
	if w.task.Stage == domain.StageWaitingApproval || w.task.Stage == domain.StageWaitingManualMerge {
		return nil
	}
	if result.Ready {
		w.task.Stage = domain.StageFinalizing
		return nil
	}
	if w.task.Project.HostingProvider() == domain.ProviderGitHub && strings.HasPrefix(w.task.Message, "等待 GitHub merge queue") {
		w.task.Stage = domain.StagePartialMerge
		if err := w.persist(); err != nil {
			return err
		}
		w.waitTick(30 * time.Second)
		return nil
	}
	w.task.Stage = domain.StagePartialMerge
	if err := w.persist(); err != nil {
		return err
	}
	w.previousPhase = domain.StagePartialMerge
	w.task.Stage = domain.StageBlocked
	return nil
}

func (w *workTask) stageManualMerging(result StepResult) error {
	if result.Ready {
		w.task.Stage = domain.StageFinalizing
	} else {
		w.previousPhase = domain.StageManualMerging
		w.task.Stage = domain.StageBlocked
	}
	return nil
}

func (w *workTask) stageFinalizing() error {
	if w.task.Stage == domain.StageWaitingApproval {
		return nil
	}
	w.task.Stage = domain.StageCompleted
	if w.evergreen {
		w.task.Message = "所有仓库已合并，来源 Issue 保持打开以供复核和后续迭代"
	} else {
		w.task.Message = "所有仓库已处理完成，来源 Issue 已关闭"
	}
	return nil
}
