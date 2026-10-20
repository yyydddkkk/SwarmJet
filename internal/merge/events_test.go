package merge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/github"
)

type eventPublishingStore struct {
	*fakeStore
	hiveID string
	pr     int64
	calls  int
}

func (s *eventPublishingStore) PublishPREvent(_ context.Context, hiveID string, pr int64) error {
	s.hiveID = hiveID
	s.pr = pr
	s.calls++
	return nil
}

func TestSuccessfulMergePublishesPREvent(t *testing.T) {
	backend := &eventPublishingStore{fakeStore: &fakeStore{hive: testHive()}}
	svc := newTestService(t, readyStub(), backend, &fakeAttestations{latest: passAttestation()}, methodologyWith(requireAllPolicy()), nil)
	outcome, err := svc.Merge(context.Background(), testHive(), 7)
	if err != nil || !outcome.Merged || backend.calls != 1 || backend.hiveID != testHive().ID || backend.pr != 7 {
		t.Fatalf("outcome=%+v err=%v notification=%+v", outcome, err, backend)
	}
}

// mergeStateGitHub keeps gate checks real while controlling submission and the
// follow-up read independently: a read failure cannot undo a confirmed merge.
type mergeStateGitHub struct {
	GitHub
	result   github.MergeResult
	state    github.MergeResult
	stateErr error
}

func (f *mergeStateGitHub) RequestMerge(context.Context, int64, int64, string, string) (github.MergeResult, error) {
	return f.result, nil
}

func (f *mergeStateGitHub) GetMergeState(context.Context, int64, int64, string) (github.MergeResult, error) {
	return f.state, f.stateErr
}

func TestMergeEventPublicationAcrossStateReads(t *testing.T) {
	readErr := errors.New("merge state unavailable")
	for _, tc := range []struct {
		name           string
		result         github.MergeResult
		state          github.MergeResult
		stateErr       error
		merged, queued bool
		calls          int
	}{
		{"confirmed success", github.MergeResult{Merged: true}, github.MergeResult{Merged: true}, nil, true, false, 1},
		{"known success with failed read", github.MergeResult{Merged: true}, github.MergeResult{}, readErr, true, false, 1},
		{"queue became merged", github.MergeResult{Queued: true}, github.MergeResult{Merged: true}, nil, true, false, 1},
		{"still queued", github.MergeResult{Queued: true}, github.MergeResult{Queued: true}, nil, false, true, 0},
		{"queue with failed read", github.MergeResult{Queued: true}, github.MergeResult{}, readErr, false, true, 0},
	} {
		for _, pinHead := range []bool{false, true} {
			name := tc.name + "/Merge"
			if pinHead {
				name += "Head"
			}
			t.Run(name, func(t *testing.T) {
				backend := &eventPublishingStore{fakeStore: &fakeStore{hive: testHive()}}
				svc := newTestService(t, readyStub(), backend, &fakeAttestations{latest: passAttestation()}, methodologyWith(requireAllPolicy()), nil)
				client, id, err := svc.github.GitHub(context.Background(), testHive().Repository)
				if err != nil {
					t.Fatal(err)
				}
				svc.github = StaticSource{Client: &mergeStateGitHub{GitHub: client, result: tc.result, state: tc.state, stateErr: tc.stateErr}, RepositoryID: id}
				var outcome Outcome
				if pinHead {
					outcome, err = svc.MergeHead(context.Background(), testHive(), 7, "head")
				} else {
					outcome, err = svc.Merge(context.Background(), testHive(), 7)
				}
				if !errors.Is(err, tc.stateErr) {
					t.Fatalf("error=%v, want cause %v", err, tc.stateErr)
				}
				if tc.stateErr != nil && !strings.Contains(err.Error(), "merge was submitted but its state could not be read") {
					t.Fatalf("state error semantics changed: %v", err)
				}
				if outcome.Merged != tc.merged || outcome.Queued != tc.queued || backend.calls != tc.calls {
					t.Fatalf("outcome=%+v notification calls=%d; want merged=%t queued=%t calls=%d", outcome, backend.calls, tc.merged, tc.queued, tc.calls)
				}
				if tc.calls > 0 && (backend.hiveID != testHive().ID || backend.pr != 7) {
					t.Fatalf("wrong notification: %+v", backend)
				}
			})
		}
	}
}
