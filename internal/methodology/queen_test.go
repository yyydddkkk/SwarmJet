package methodology

import (
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestValidateQueenPrompt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prompt string
		file   bool
		want   string
	}{
		{name: "optional"},
		{name: "present", prompt: "queen.md", file: true},
		{name: "missing", prompt: "queen.md", want: "is not one of the files"},
		{name: "traversal", prompt: "../queen.md", want: "queen prompt"},
		{name: "manifest reserved", prompt: ManifestName, want: "reserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := validManifest()
			manifest.Queen = domain.QueenStyle{Prompt: tc.prompt, Merge: true}
			files := validFiles()
			if tc.file {
				files = append(files, domain.MethodologyFile{Path: tc.prompt, Content: "style"})
			}
			err := Validate(manifest, files)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
