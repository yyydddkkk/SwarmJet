package catalog

import (
	"errors"
	"path"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/methodology"
	"github.com/yyydddkkk/SwarmJet/methodologies"
)

func TestCatalogTemplates(t *testing.T) {
	templates, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 2 {
		t.Fatalf("got %d templates, want pstack and minimal", len(templates))
	}
	listed := make(map[string]bool)
	for _, template := range templates {
		listed[template.Name] = true
		metadata, manifest, files, err := Get(template.Name)
		if err != nil {
			t.Fatal(err)
		}
		if metadata != template {
			t.Fatalf("Get(%q) metadata = %+v, want %+v", template.Name, metadata, template)
		}
		if err := methodology.Validate(manifest, files); err != nil {
			t.Fatalf("%s: %v", template.Name, err)
		}
		for _, file := range files {
			if path.Base(file.Path) == "README.md" || file.Path == methodology.ManifestName {
				t.Errorf("%s includes unpublished file %s", template.Name, file.Path)
			}
		}
		if template.Name == "minimal" && (len(manifest.Roles) != 1 || !manifest.Roles["worker"].Permissions.Publish || !manifest.Roles["worker"].Permissions.Message || manifest.Merge.RequireAttestation || manifest.Merge.Auto) {
			t.Fatalf("unexpected minimal workflow: %+v", manifest)
		}
		if template.Name == "pstack" && manifest.Queen.Prompt != "queen.md" {
			t.Fatalf("pstack queen prompt = %q", manifest.Queen.Prompt)
		}
	}
	entries, err := methodologies.Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() && !listed[entry.Name()] {
			t.Errorf("template directory %s is missing from catalog", entry.Name())
		}
	}
	if !listed["minimal"] || !listed["pstack"] {
		t.Fatalf("missing required template: %+v", listed)
	}
}

func TestGetUnknownTemplate(t *testing.T) {
	for _, name := range []string{"missing", "", "../pstack", "pstack/../minimal"} {
		if _, _, _, err := Get(name); !errors.Is(err, ErrNotFound) {
			t.Errorf("Get(%q) error = %v, want ErrNotFound", name, err)
		}
	}
}

func TestReturnedTemplatesAreIndependent(t *testing.T) {
	templates, err := List()
	if err != nil {
		t.Fatal(err)
	}
	templates[0].Name = "mutated"
	_, manifest, files, err := Get("minimal")
	if err != nil {
		t.Fatal(err)
	}
	delete(manifest.Roles, "worker")
	files[0].Content = "mutated"
	_, again, againFiles, err := Get("minimal")
	if err != nil || len(again.Roles) != 1 || againFiles[0].Content == "mutated" {
		t.Fatalf("catalog mutated across requests: manifest=%+v files=%+v err=%v", again, againFiles, err)
	}
}
