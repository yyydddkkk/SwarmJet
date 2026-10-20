package main

import (
	"context"
	"errors"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
	"github.com/yyydddkkk/SwarmJet/internal/merge"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// activeMethodologyStore is the contract method the methodology task adds to
// *store.Store. Keeping the assertion here means this branch builds and runs
// before that task lands, and starts reading the methodology as soon as the
// method exists.
type activeMethodologyStore interface {
	ActiveMethodology(ctx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error)
}

type methodologyStore struct {
	s *store.Store
}

func (m methodologyStore) ActiveMethodology(ctx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error) {
	if m.s == nil {
		return domain.Methodology{}, nil, domain.ErrNotFound
	}
	reader, ok := any(m.s).(activeMethodologyStore)
	if !ok {
		return domain.Methodology{}, nil, domain.ErrNotFound
	}
	return reader.ActiveMethodology(ctx, hiveID)
}

// mergeGitHubSource resolves a Hive repository and mints a repository-scoped
// installation token for the merge service. Tokens are short-lived and stay in
// memory.
type mergeGitHubSource struct {
	repos *githubRepositories
}

func (s mergeGitHubSource) GitHub(ctx context.Context, repository string) (merge.GitHub, int64, error) {
	if s.repos == nil {
		return nil, 0, errors.New("GitHub App is not configured")
	}
	id, err := s.repos.Resolve(ctx, repository)
	if err != nil {
		return nil, 0, err
	}
	token, err := s.repos.Token(ctx, repository)
	if err != nil {
		return nil, 0, err
	}
	return github.New(func(context.Context) (string, error) { return token, nil }), id, nil
}

var _ merge.GitHub = (*github.Client)(nil)
