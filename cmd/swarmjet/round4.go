package main

import (
	"context"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
	"github.com/yyydddkkk/SwarmJet/internal/router"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// round4Verdicts adapts *store.Store to merge.VerdictReader. The verdict table
// and its methods land in the parallel data-layer task; the interface assertion
// keeps this branch buildable now and makes the adapter work unchanged once the
// methods exist.
type round4Verdicts struct {
	s *store.Store
}

type verdictStore interface {
	LatestVerdict(ctx context.Context, hiveID string, pr int64) (domain.Verdict, error)
	Verdicts(ctx context.Context, hiveID string, pr int64) ([]domain.Verdict, error)
}

func (r round4Verdicts) store() (verdictStore, bool) {
	if r.s == nil {
		return nil, false
	}
	v, ok := any(r.s).(verdictStore)
	return v, ok
}

func (r round4Verdicts) LatestVerdict(ctx context.Context, hiveID string, pr int64) (domain.Verdict, error) {
	if v, ok := r.store(); ok {
		return v.LatestVerdict(ctx, hiveID, pr)
	}
	return domain.Verdict{}, domain.ErrNotFound
}

func (r round4Verdicts) Verdicts(ctx context.Context, hiveID string, pr int64) ([]domain.Verdict, error) {
	if v, ok := r.store(); ok {
		return v.Verdicts(ctx, hiveID, pr)
	}
	return []domain.Verdict{}, nil
}

// onChecksFunc is the contract signature of router.Options.OnChecks.
type onChecksFunc func(context.Context, domain.Hive, int64, string, string) error

// mergeGitHubSource mints a repository-scoped installation token and returns a
// GitHub client that satisfies merge.GitHub. The token is resolved per call, so
// a rotated App credential takes effect without restarting serve.
type mergeGitHubSource struct {
	repos *githubRepositories
}

func (s mergeGitHubSource) GitHub(ctx context.Context, repository string) (merge.GitHub, error) {
	token, err := s.repos.Token(ctx, repository)
	if err != nil {
		return nil, err
	}
	return github.New(func(context.Context) (string, error) { return token, nil }), nil
}

// mergeRouterOptions is the single wiring point for the merge check callback.
// Task B adds OnHead/OnChecks to router.Options; the merge check then attaches
// here, next to the Router construction:
//
//	opts.OnChecks = checks.OnChecks
//
// The assertion below keeps Scheduler.OnChecks in sync with that contract
// signature until the field exists.
func mergeRouterOptions(slug func(context.Context) string, checks *merge.Scheduler) router.Options {
	opts := router.Options{AppSlugSource: slug}
	if checks != nil {
		var onChecks onChecksFunc = checks.OnChecks
		_ = onChecks
	}
	return opts
}
