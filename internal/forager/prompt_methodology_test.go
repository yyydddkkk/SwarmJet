package forager

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type mutableMethodologySource struct {
	mu    sync.Mutex
	m     domain.Methodology
	files []domain.MethodologyFile
	err   error
}

func (s *mutableMethodologySource) ActiveMethodology(context.Context, string) (domain.Methodology, []domain.MethodologyFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m, s.files, s.err
}

func (s *mutableMethodologySource) set(m domain.Methodology) {
	s.mu.Lock()
	s.m = m
	s.mu.Unlock()
}

func promptTestManifest(order string) domain.MethodologyManifest {
	return domain.MethodologyManifest{
		DefaultRole:    "worker",
		StandingOrders: []string{order},
		Roles: map[string]domain.Role{
			"worker": {
				Description: "Write code and open a pull request.",
				Permissions: domain.RolePermissions{Publish: true, Message: true},
			},
		},
	}
}

func TestBuildPromptStartsWithStandingRulesAndRefreshesEveryTurn(t *testing.T) {
	source := &mutableMethodologySource{
		m: domain.Methodology{HiveID: testHiveID, Version: 1, Manifest: promptTestManifest("first standing order")},
	}
	SetMethodologySource(source)
	t.Cleanup(func() { SetMethodologySource(nil) })

	agent := domain.Agent{ID: testAgentID, HiveID: testHiveID, RoleName: "worker", Task: "fix it", Branch: testBranch}
	hive := domain.Hive{ID: testHiveID, Name: "Acme", Repository: "acme/widgets"}
	first := buildPrompt(agent, hive, nil, true)
	if !strings.HasPrefix(first, "## Hive methodology") {
		t.Fatalf("prompt does not start with the methodology header:\n%s", first)
	}
	for _, want := range []string{"worker", "Write code and open a pull request.", "first standing order", "publish=true", "message=true"} {
		if !strings.Contains(first, want) {
			t.Fatalf("prompt missing %q:\n%s", want, first)
		}
	}
	if rule, intro := strings.Index(first, "first standing order"), strings.Index(first, "You are Forager"); rule < 0 || intro < 0 || rule > intro {
		t.Fatalf("standing rules must come before the task framing:\n%s", first)
	}

	// A newly active version is used by the very next turn.
	source.set(domain.Methodology{HiveID: testHiveID, Version: 2, Manifest: promptTestManifest("second standing order")})
	second := buildPrompt(agent, hive, nil, false)
	if !strings.Contains(second, "second standing order") {
		t.Fatalf("next prompt did not use the new standing order:\n%s", second)
	}
	if strings.Contains(second, "first standing order") {
		t.Fatalf("next prompt kept the old standing order:\n%s", second)
	}
	if !strings.HasPrefix(second, "## Hive methodology") {
		t.Fatalf("continuation prompt has no header:\n%s", second)
	}
}

func TestBuildPromptFallsBackToBuiltin(t *testing.T) {
	SetMethodologySource(&mutableMethodologySource{err: domain.ErrNotFound})
	t.Cleanup(func() { SetMethodologySource(nil) })

	agent := domain.Agent{ID: testAgentID, HiveID: testHiveID, Task: "fix it", Branch: testBranch}
	hive := domain.Hive{ID: testHiveID, Name: "Acme", Repository: "acme/widgets"}
	text := buildPrompt(agent, hive, nil, true)
	if !strings.HasPrefix(text, "## Hive methodology") {
		t.Fatalf("prompt has no builtin header:\n%s", text)
	}
	if !strings.Contains(text, `"worker"`) {
		t.Fatalf("builtin default role missing from prompt:\n%s", text)
	}
}
