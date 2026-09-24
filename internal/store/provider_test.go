package store

import (
	"context"
	"swarmjet/internal/domain"
	"testing"
)

func TestIssueSourceCompatibility(t *testing.T) {
	issue := domain.Issue{ProjectID: 9, IID: 1}
	if got := issueSource("https://git.example/api/v4/", issue); got != "https://git.example/9/1" {
		t.Fatal(got)
	}
	issue.Provider = domain.ProviderGitHub
	if got := issueSource("", issue); got != "github/9/1" {
		t.Fatal(got)
	}
}

func TestGitHubConnectionPersistsWithoutInstallationToken(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	c, err := s.SaveConnection(ctx, domain.Connection{ID: "github", Name: "GitHub", Provider: domain.ProviderGitHub, GitHubInstallationID: 42}, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Connection(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.HostingProvider() != domain.ProviderGitHub || got.GitHubInstallationID != 42 {
		t.Fatalf("metadata lost: %+v", got)
	}
	secret, err := s.ConnectionSecrets(ctx, c.ID)
	if err != nil || secret != "" {
		t.Fatalf("unexpected secret: %q %v", secret, err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	got, err = s.Connection(ctx, c.ID)
	if err != nil || got.GitHubInstallationID != 42 {
		t.Fatal("repeat migration changed metadata", err)
	}
}
