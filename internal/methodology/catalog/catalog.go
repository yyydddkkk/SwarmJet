// Package catalog loads the optional methodology templates shipped with the
// platform. Listing templates never installs one in a Hive.
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology"
	"github.com/yyydddkkk/SwarmJet/methodologies"
	"go.yaml.in/yaml/v3"
)

// ErrNotFound means the requested name is not in the built-in catalog.
var ErrNotFound = errors.New("methodology template not found")

// List returns template metadata in catalog order. Each call returns its own
// slice, so a caller cannot mutate the catalog for other requests.
func List() ([]domain.MethodologyTemplate, error) {
	raw, err := methodologies.Files.ReadFile("catalog.yaml")
	if err != nil {
		return nil, fmt.Errorf("methodology catalog: %w", err)
	}
	var entries []struct {
		Name        string `yaml:"name"`
		Title       string `yaml:"title"`
		Description string `yaml:"description"`
		Version     string `yaml:"version"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("methodology catalog: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("methodology catalog: expected one YAML document")
	}
	templates := make([]domain.MethodologyTemplate, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.ContainsAny(entry.Name, "/\\") || entry.Name != strings.TrimSpace(entry.Name) {
			return nil, fmt.Errorf("methodology catalog: invalid template name %q", entry.Name)
		}
		if seen[entry.Name] || strings.TrimSpace(entry.Title) == "" || strings.TrimSpace(entry.Description) == "" || strings.TrimSpace(entry.Version) == "" {
			return nil, fmt.Errorf("methodology catalog: duplicate or incomplete entry %q", entry.Name)
		}
		seen[entry.Name] = true
		templates = append(templates, domain.MethodologyTemplate{
			Name: entry.Name, Title: entry.Title, Description: entry.Description, Version: entry.Version,
		})
	}
	return templates, nil
}

// Get returns metadata, the parsed manifest, and every published template file.
// Human README.md files and the manifest itself are omitted from files. The
// package is validated with the same rules used for publishing a version.
func Get(name string) (domain.MethodologyTemplate, domain.MethodologyManifest, []domain.MethodologyFile, error) {
	var metadata domain.MethodologyTemplate
	var manifest domain.MethodologyManifest
	templates, err := List()
	if err != nil {
		return metadata, manifest, nil, err
	}
	found := false
	for _, candidate := range templates {
		if candidate.Name == name {
			metadata = candidate
			found = true
			break
		}
	}
	if !found {
		return metadata, manifest, nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	raw, err := methodologies.Files.ReadFile(path.Join(name, methodology.ManifestName))
	if err != nil {
		return metadata, manifest, nil, fmt.Errorf("methodology catalog %q: %w", name, err)
	}
	manifest, err = methodology.Parse(raw)
	if err != nil {
		return metadata, manifest, nil, err
	}
	files := make([]domain.MethodologyFile, 0)
	err = fs.WalkDir(methodologies.Files, name, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		relative := strings.TrimPrefix(filePath, name+"/")
		if relative == methodology.ManifestName || entry.Name() == "README.md" {
			return nil
		}
		content, err := methodologies.Files.ReadFile(filePath)
		if err != nil {
			return err
		}
		files = append(files, domain.MethodologyFile{Path: relative, Content: string(content)})
		return nil
	})
	if err != nil {
		return metadata, manifest, nil, fmt.Errorf("methodology catalog %q: %w", name, err)
	}
	if err := methodology.Validate(manifest, files); err != nil {
		return metadata, manifest, nil, fmt.Errorf("methodology catalog %q: %w", name, err)
	}
	return metadata, manifest, files, nil
}
