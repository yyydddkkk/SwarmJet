package platformwrite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/forge"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

type fakeIntents struct {
	intent            store.KubernetesWriteIntent
	authorized        bool
	records, confirms int
}

func (f *fakeIntents) RecordKubernetesWriteIntent(_ context.Context, w store.KubernetesWriteIntent, _ string) (store.KubernetesWriteIntent, error) {
	f.records++
	if !f.authorized {
		return w, store.ErrExecutionAuthority
	}
	if f.intent.ID == "" {
		w.State = "pending"
		f.intent = w
	}
	return f.intent, nil
}
func (f *fakeIntents) WithKubernetesAuthority(ctx context.Context, _, _ string, _ int64, _ string, fn func(context.Context) error) error {
	if !f.authorized {
		return store.ErrExecutionAuthority
	}
	return fn(ctx)
}
func (f *fakeIntents) ConfirmKubernetesWriteIntent(_ context.Context, _, _, _ string, _ int64, remoteID int64) error {
	if !f.authorized {
		return store.ErrExecutionAuthority
	}
	f.confirms++
	f.intent.State = "confirmed"
	f.intent.RemoteID = remoteID
	return nil
}

type fakePlatform struct {
	branch                          string
	mr                              forge.MergeRequest
	pushes, creates, merges         int
	losePush, loseCreate, loseMerge bool
}

func (f *fakePlatform) BranchSHA(context.Context, int64, string) (string, bool, error) {
	return f.branch, f.branch != "", nil
}
func (f *fakePlatform) PushFixedSHA(_ context.Context, w store.KubernetesWriteIntent) error {
	f.pushes++
	f.branch = w.HeadSHA
	if f.losePush {
		return errors.New("push response lost")
	}
	return nil
}
func (f *fakePlatform) FindMR(_ context.Context, _ int64, source, target string) (*forge.MergeRequest, error) {
	if f.mr.IID != 0 && f.mr.SourceBranch == source && f.mr.TargetBranch == target && f.mr.State == "opened" {
		m := f.mr
		return &m, nil
	}
	return nil, nil
}
func (f *fakePlatform) CreateMR(_ context.Context, _ int64, source, target, _, _ string) (forge.MergeRequest, error) {
	f.creates++
	f.mr = forge.MergeRequest{IID: 7, SourceBranch: source, TargetBranch: target, SHA: f.branch, State: "opened"}
	if f.loseCreate {
		return forge.MergeRequest{}, errors.New("MR response lost")
	}
	return f.mr, nil
}
func (f *fakePlatform) GetMR(context.Context, int64, int64) (forge.MergeRequest, error) {
	return f.mr, nil
}
func (f *fakePlatform) Merge(_ context.Context, _ int64, _ int64, _, _ string) error {
	f.merges++
	f.mr.State = "merged"
	if f.loseMerge {
		return errors.New("merge response lost")
	}
	return nil
}

func testIntent(kind string) store.KubernetesWriteIntent {
	return store.KubernetesWriteIntent{ID: "intent-" + kind, TaskID: "task", ExecutionID: "exec", AuthorityVersion: 1, Kind: kind, RepositoryID: "repo", ProjectID: 5, SourceBranch: "swarmjet/task", TargetBranch: "main", HeadSHA: strings.Repeat("a", 40)}
}

func TestLostResponsesReconcileWithoutDuplicateWrites(t *testing.T) {
	ctx := context.Background()
	f := &fakeIntents{authorized: true}
	p := &fakePlatform{losePush: true, loseCreate: true, loseMerge: true}
	r := Reconciler{Intents: f, Platform: p}
	for _, kind := range []string{"push", "mr", "merge"} {
		f.intent = store.KubernetesWriteIntent{}
		req := Request{Intent: testIntent(kind), AttemptID: "pod-1", MRID: 7, Title: "Change"}
		id, err := r.Execute(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if kind == "push" && id != 0 || kind != "push" && id != 7 {
			t.Fatalf("%s remote ID %d", kind, id)
		}
		if _, err = r.Execute(ctx, req); err != nil {
			t.Fatalf("retry %s: %v", kind, err)
		}
	}
	if p.pushes != 1 || p.creates != 1 || p.merges != 1 || f.confirms != 3 {
		t.Fatalf("duplicate mutation: pushes=%d creates=%d merges=%d confirmations=%d", p.pushes, p.creates, p.merges, f.confirms)
	}
}

func TestStaleAuthorityCannotWrite(t *testing.T) {
	f := &fakeIntents{}
	p := &fakePlatform{}
	_, err := (Reconciler{Intents: f, Platform: p}).Execute(context.Background(), Request{Intent: testIntent("push"), AttemptID: "stale"})
	if !errors.Is(err, store.ErrExecutionAuthority) || p.pushes != 0 {
		t.Fatalf("stale execution wrote: %v", err)
	}
}

func TestChangedMRHeadRejected(t *testing.T) {
	f := &fakeIntents{authorized: true}
	p := &fakePlatform{branch: strings.Repeat("b", 40)}
	_, err := (Reconciler{Intents: f, Platform: p}).Execute(context.Background(), Request{Intent: testIntent("mr"), AttemptID: "pod", Title: "Change"})
	if !errors.Is(err, ErrRemoteConflict) || p.creates != 0 {
		t.Fatalf("changed head accepted: %v", err)
	}
}

func TestTrustedPusherRejectsUntrustedConfiguration(t *testing.T) {
	dir := t.TempDir()
	w := testIntent("push")
	p := GitPusher{Repositories: map[string]TrustedRepo{"repo": {ProjectID: 5, Dir: dir, URL: "https://user:secret@example.com/repo.git"}}}
	if err := p.PushFixedSHA(context.Background(), w); err == nil {
		t.Fatal("embedded URL credential accepted")
	}
	p.Repositories["repo"] = TrustedRepo{ProjectID: 5, Dir: dir, URL: "https://example.com/repo.git"}
	p.AuthEnv = map[string]string{"GIT_CONFIG_COUNT": "1"}
	if _, err := p.git(context.Background(), dir, "version"); err == nil {
		t.Fatal("Git config override accepted")
	}
}
