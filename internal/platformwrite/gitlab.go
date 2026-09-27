package platformwrite

import (
	"context"
	"errors"

	"swarmjet/internal/forge"
	"swarmjet/internal/gitlab"
	"swarmjet/internal/store"
)

// FixedSHAPusher is implemented by a control-plane Git transport. Its
// credentials and workspace must never be mounted in a Runner Pod.
type FixedSHAPusher interface {
	PushFixedSHA(context.Context, store.KubernetesWriteIntent) error
}

type GitLabPlatform struct {
	Client *gitlab.Client
	Pusher FixedSHAPusher
}

func (p GitLabPlatform) BranchSHA(ctx context.Context, projectID int64, branch string) (string, bool, error) {
	b, err := p.Client.GetBranch(ctx, projectID, branch)
	if err != nil {
		var apiErr *gitlab.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return "", false, nil
		}
		return "", false, err
	}
	return b.Commit.ID, true, nil
}

func (p GitLabPlatform) PushFixedSHA(ctx context.Context, w store.KubernetesWriteIntent) error {
	if p.Pusher == nil {
		return errors.New("trusted Git pusher is not configured")
	}
	return p.Pusher.PushFixedSHA(ctx, w)
}
func (p GitLabPlatform) FindMR(ctx context.Context, projectID int64, source, target string) (*forge.MergeRequest, error) {
	return p.Client.FindMRAnyState(ctx, projectID, source, target)
}
func (p GitLabPlatform) CreateMR(ctx context.Context, projectID int64, source, target, title, description string) (forge.MergeRequest, error) {
	return p.Client.CreateMR(ctx, projectID, source, target, title, description)
}
func (p GitLabPlatform) GetMR(ctx context.Context, projectID, iid int64) (forge.MergeRequest, error) {
	return p.Client.GetMR(ctx, projectID, iid)
}
func (p GitLabPlatform) Merge(ctx context.Context, projectID, iid int64, sha, target string) error {
	return p.Client.Merge(ctx, projectID, iid, sha, target)
}

var _ Platform = GitLabPlatform{}
