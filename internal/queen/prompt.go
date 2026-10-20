// Package queen provides the platform-owned instructions for a Hive's Queen.
package queen

import (
	"context"
	_ "embed"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/methodology"
)

//go:embed core.md
var core string

// Header returns the Queen's instructions for this turn. The platform core is
// always present; the active methodology's Queen prompt is appended when it can
// be read. Missing methodology or prompt files, and source errors, leave the
// core intact so the Queen can still help recover the Hive.
func Header(ctx context.Context, source methodology.ActiveSource, hiveID string) string {
	if source == nil {
		return core
	}
	active, files, err := source.ActiveMethodology(ctx, hiveID)
	if err != nil || active.Manifest.Queen.Prompt == "" {
		return core
	}
	for _, file := range files {
		if file.Path == active.Manifest.Queen.Prompt {
			if strings.TrimSpace(file.Content) == "" {
				return core
			}
			return core + "\n## Methodology Queen style\n\n" + file.Content
		}
	}
	return core
}
