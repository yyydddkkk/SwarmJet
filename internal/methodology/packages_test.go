package methodology

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// LoadDir reads a methodology package from disk the way a local Agent would
// publish it: methodology.yaml is the manifest, every other file is delivered.
// README.md files are documentation for humans and are not published.
func loadPackage(t *testing.T, dir string) (domain.MethodologyManifest, []domain.MethodologyFile) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := Parse(raw)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	var files []domain.MethodologyFile
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == ManifestName || filepath.Base(rel) == "README.md" {
			return nil
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files = append(files, domain.MethodologyFile{Path: rel, Content: string(content)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return manifest, files
}

// Every methodology shipped in the repository must pass the same validation
// publish_methodology applies, and every skill must carry the frontmatter omp
// needs to discover it.
func TestShippedMethodologiesValidate(t *testing.T) {
	dirs, err := filepath.Glob("../../methodologies/*/" + ManifestName)
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no shipped methodologies found (err %v)", err)
	}
	for _, manifestPath := range dirs {
		dir := filepath.Dir(manifestPath)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			manifest, files := loadPackage(t, dir)
			if err := Validate(manifest, files); err != nil {
				t.Fatal(err)
			}
			for _, f := range files {
				if !strings.HasPrefix(f.Path, "skills/") || !strings.HasSuffix(f.Path, "/SKILL.md") {
					continue
				}
				name := strings.TrimSuffix(strings.TrimPrefix(f.Path, "skills/"), "/SKILL.md")
				if strings.Contains(name, "/") {
					t.Errorf("%s: omp only discovers skills one level under skills/", f.Path)
				}
				if !strings.HasPrefix(f.Content, "---\n") || !strings.Contains(f.Content, "\nname: "+name+"\n") || !strings.Contains(f.Content, "\ndescription: ") {
					t.Errorf("%s: needs frontmatter with name %q and a description", f.Path, name)
				}
			}
		})
	}
}
