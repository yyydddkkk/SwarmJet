// Package platformwrite executes fixed remote writes in the trusted control
// plane. Runner Pods receive no GitLab write credential.
package platformwrite

import (
	"context"
	"errors"
	"fmt"

	"swarmjet/internal/forge"
	"swarmjet/internal/store"
)

var ErrRemoteConflict = errors.New("remote state differs from fixed write intent")

type Intents interface {
	RecordKubernetesWriteIntent(context.Context, store.KubernetesWriteIntent, string) (store.KubernetesWriteIntent, error)
	ConfirmKubernetesWriteIntent(context.Context, string, string, string, int64, int64) error
	WithKubernetesAuthority(context.Context, string, string, int64, string, func(context.Context) error) error
}

// Platform is constructed with control-plane credentials. PushFixedSHA must
// publish only HeadSHA to SourceBranch and must reject non-fast-forward pushes.
type Platform interface {
	BranchSHA(context.Context, int64, string) (sha string, exists bool, err error)
	PushFixedSHA(context.Context, store.KubernetesWriteIntent) error
	FindMR(context.Context, int64, string, string) (*forge.MergeRequest, error)
	CreateMR(context.Context, int64, string, string, string, string) (forge.MergeRequest, error)
	GetMR(context.Context, int64, int64) (forge.MergeRequest, error)
	Merge(context.Context, int64, int64, string, string) error
}

type Request struct {
	Intent    store.KubernetesWriteIntent
	AttemptID string
	// MRID is required for merge. The fetched MR must match the fixed intent.
	MRID        int64
	Title       string
	Description string
}

type Reconciler struct {
	Intents  Intents
	Platform Platform
}

// Execute records intent before any remote mutation. A retry observes remote
// state first; an ambiguous write response triggers another remote observation.
func (r Reconciler) Execute(ctx context.Context, req Request) (int64, error) {
	if r.Intents == nil || r.Platform == nil || req.AttemptID == "" {
		return 0, errors.New("trusted platform write is not configured")
	}
	w, err := r.Intents.RecordKubernetesWriteIntent(ctx, req.Intent, req.AttemptID)
	if err != nil {
		return 0, err
	}
	if w.State == "confirmed" {
		return r.confirmedRemote(ctx, w)
	}
	if w.State != "pending" {
		return 0, errors.New("write intent is not pending")
	}
	var remoteID int64
	err = r.Intents.WithKubernetesAuthority(ctx, w.TaskID, w.ExecutionID, w.AuthorityVersion, req.AttemptID, func(ctx context.Context) error {
		var e error
		remoteID, e = r.reconcile(ctx, w, req)
		return e
	})
	if err != nil {
		return 0, err
	}
	if err = r.Intents.ConfirmKubernetesWriteIntent(ctx, w.ID, w.ExecutionID, req.AttemptID, w.AuthorityVersion, remoteID); err != nil {
		return 0, err
	}
	return remoteID, nil
}

func (r Reconciler) confirmedRemote(ctx context.Context, w store.KubernetesWriteIntent) (int64, error) {
	switch w.Kind {
	case "push":
		sha, exists, err := r.Platform.BranchSHA(ctx, w.ProjectID, w.SourceBranch)
		if err != nil {
			return 0, err
		}
		if !exists || sha != w.HeadSHA {
			return 0, ErrRemoteConflict
		}
		return 0, nil
	case "mr", "merge":
		if w.RemoteID < 1 {
			return 0, ErrRemoteConflict
		}
		mr, err := r.Platform.GetMR(ctx, w.ProjectID, w.RemoteID)
		if err != nil {
			return 0, err
		}
		id, err := checkedMR(w, mr)
		if err != nil {
			return 0, err
		}
		if w.Kind == "mr" && mr.State != "opened" && mr.State != "merged" {
			return 0, ErrRemoteConflict
		}
		if w.Kind == "merge" && mr.State != "merged" {
			return 0, ErrRemoteConflict
		}
		return id, nil
	default:
		return 0, ErrRemoteConflict
	}
}

func (r Reconciler) reconcile(ctx context.Context, w store.KubernetesWriteIntent, req Request) (int64, error) {
	switch w.Kind {
	case "push":
		sha, exists, err := r.Platform.BranchSHA(ctx, w.ProjectID, w.SourceBranch)
		if err != nil {
			return 0, err
		}
		if exists && sha == w.HeadSHA {
			return 0, nil
		}
		if err = r.Platform.PushFixedSHA(ctx, w); err != nil {
			if observed, ok, readErr := r.Platform.BranchSHA(ctx, w.ProjectID, w.SourceBranch); readErr == nil && ok && observed == w.HeadSHA {
				return 0, nil
			}
			return 0, err
		}
		sha, exists, err = r.Platform.BranchSHA(ctx, w.ProjectID, w.SourceBranch)
		if err != nil {
			return 0, err
		}
		if !exists || sha != w.HeadSHA {
			return 0, ErrRemoteConflict
		}
		return 0, nil
	case "mr":
		sha, exists, err := r.Platform.BranchSHA(ctx, w.ProjectID, w.SourceBranch)
		if err != nil {
			return 0, err
		}
		if !exists || sha != w.HeadSHA {
			return 0, ErrRemoteConflict
		}
		mr, err := r.Platform.FindMR(ctx, w.ProjectID, w.SourceBranch, w.TargetBranch)
		if err != nil {
			return 0, err
		}
		if mr != nil {
			if mr.State != "opened" && mr.State != "merged" {
				return 0, ErrRemoteConflict
			}
			return checkedMR(w, *mr)
		}
		if req.Title == "" {
			return 0, errors.New("merge request title required")
		}
		created, err := r.Platform.CreateMR(ctx, w.ProjectID, w.SourceBranch, w.TargetBranch, req.Title, req.Description)
		if err != nil {
			mr, readErr := r.Platform.FindMR(ctx, w.ProjectID, w.SourceBranch, w.TargetBranch)
			if readErr == nil && mr != nil {
				if mr.State != "opened" && mr.State != "merged" {
					return 0, ErrRemoteConflict
				}
				return checkedMR(w, *mr)
			}
			return 0, err
		}
		return checkedMR(w, created)
	case "merge":
		if req.MRID < 1 {
			return 0, errors.New("merge request ID required")
		}
		mr, err := r.Platform.GetMR(ctx, w.ProjectID, req.MRID)
		if err != nil {
			return 0, err
		}
		if _, err = checkedMR(w, mr); err != nil {
			return 0, err
		}
		if mr.State == "merged" {
			return mr.IID, nil
		}
		if mr.State != "opened" {
			return 0, ErrRemoteConflict
		}
		if err = r.Platform.Merge(ctx, w.ProjectID, mr.IID, w.HeadSHA, w.TargetBranch); err != nil {
			observed, readErr := r.Platform.GetMR(ctx, w.ProjectID, mr.IID)
			if readErr == nil && observed.State == "merged" {
				return checkedMR(w, observed)
			}
			return 0, err
		}
		observed, err := r.Platform.GetMR(ctx, w.ProjectID, mr.IID)
		if err != nil {
			return 0, err
		}
		if observed.State != "merged" {
			return 0, ErrRemoteConflict
		}
		return checkedMR(w, observed)
	default:
		return 0, fmt.Errorf("unsupported write kind %q", w.Kind)
	}
}

func checkedMR(w store.KubernetesWriteIntent, mr forge.MergeRequest) (int64, error) {
	if mr.IID < 1 || mr.SourceBranch != w.SourceBranch || mr.TargetBranch != w.TargetBranch || mr.SHA != w.HeadSHA {
		return 0, ErrRemoteConflict
	}
	return mr.IID, nil
}
