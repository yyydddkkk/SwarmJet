package queen

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type activeSourceFunc func(context.Context, string) (domain.Methodology, []domain.MethodologyFile, error)

func (f activeSourceFunc) ActiveMethodology(ctx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error) {
	return f(ctx, hiveID)
}

func TestHeaderFallsBackToCore(t *testing.T) {
	if strings.TrimSpace(core) == "" {
		t.Fatal("embedded Queen core is empty")
	}
	for _, tc := range []struct {
		name   string
		prompt string
		files  []domain.MethodologyFile
		err    error
	}{
		{name: "no active methodology", err: domain.ErrNotFound},
		{name: "read error", err: errors.New("source unavailable")},
		{name: "no Queen prompt"},
		{name: "missing file", prompt: "queen.md", files: []domain.MethodologyFile{{Path: "worker.md", Content: "Worker instructions"}}},
		{name: "empty file", prompt: "queen.md", files: []domain.MethodologyFile{{Path: "queen.md", Content: " \n"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := activeSourceFunc(func(context.Context, string) (domain.Methodology, []domain.MethodologyFile, error) {
				return domain.Methodology{Manifest: domain.MethodologyManifest{Queen: domain.QueenStyle{Prompt: tc.prompt}}}, tc.files, tc.err
			})
			if got := Header(context.Background(), source, "hive-1"); got != core {
				t.Fatalf("fallback header = %q, want embedded core", got)
			}
		})
	}
	if got := Header(context.Background(), nil, "hive-1"); got != core {
		t.Fatal("nil source did not return embedded core")
	}
}

func TestHeaderUsesCurrentMethodologyEachTurn(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "marker")
	style := "Use short reports.\n"
	calls := 0
	source := activeSourceFunc(func(gotCtx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error) {
		calls++
		if gotCtx != ctx || hiveID != "hive-1" {
			t.Fatalf("active lookup got context %v and Hive %q", gotCtx, hiveID)
		}
		return domain.Methodology{Manifest: domain.MethodologyManifest{Queen: domain.QueenStyle{Prompt: "styles/queen.md"}}}, []domain.MethodologyFile{
			{Path: "roles/worker.md", Content: "Do not append worker instructions."},
			{Path: "styles/queen.md", Content: style},
		}, nil
	})
	for _, nextStyle := range []string{style, "Use detailed reports.\n"} {
		style = nextStyle
		got := Header(ctx, source, "hive-1")
		if !strings.HasPrefix(got, core) || !strings.HasSuffix(got, style) {
			t.Fatalf("header does not retain core followed by current Queen style: %q", got)
		}
		if strings.Contains(got, "Do not append worker instructions.") {
			t.Fatal("header included another role's prompt")
		}
	}
	if calls != 2 {
		t.Fatalf("active methodology lookups = %d, want 2", calls)
	}
}
