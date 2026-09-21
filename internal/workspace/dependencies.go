package workspace

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"sort"
	"strings"
	"swarmjet/internal/domain"
)

// validateManifestDependencies keeps the console's persisted merge-order IDs in
// agreement with the pinned manifest. Metadata-only roots have no declaration.
func validateManifestDependencies(data []byte, repositories []domain.Repository) error {
	var raw struct {
		Projects map[string]yaml.Node `yaml:"projects"`
	}
	if e := yaml.Unmarshal(data, &raw); e != nil {
		return e
	}
	type declaration struct {
		Path      string   `yaml:"path"`
		DependsOn []string `yaml:"depends_on"`
		Provides  []string `yaml:"provides"`
	}
	declarations := map[string]declaration{}
	byPath := map[string]string{}
	providers := map[string][]string{}
	aliases := make([]string, 0, len(raw.Projects))
	for alias := range raw.Projects {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		node := raw.Projects[alias]
		d := declaration{Path: alias}
		if node.Kind == yaml.MappingNode {
			if e := node.Decode(&d); e != nil {
				return fmt.Errorf("meta project %s dependency metadata: %w", alias, e)
			}
			if d.Path == "" {
				d.Path = alias
			}
		} else if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
			return fmt.Errorf("invalid meta project %s", alias)
		}
		if prior, ok := byPath[d.Path]; ok {
			return fmt.Errorf("meta projects %s and %s share path %s", prior, alias, d.Path)
		}
		byPath[d.Path] = alias
		declarations[alias] = d
		for _, capability := range canonical(d.Provides) {
			providers[capability] = append(providers[capability], alias)
		}
	}
	enabled := map[string]string{}
	for _, r := range repositories {
		if r.Path == "." {
			continue
		}
		alias, ok := byPath[r.Path]
		if !ok {
			return fmt.Errorf("registered repository %s path %s is absent from meta manifest", r.ID, r.Path)
		}
		enabled[alias] = r.ID
	}
	for _, r := range repositories {
		if r.Path == "." {
			continue
		}
		alias := byPath[r.Path]
		expected := []string{}
		missing := []string{}
		for _, dependency := range canonical(declarations[alias].DependsOn) {
			target := ""
			if _, ok := declarations[dependency]; ok {
				target = dependency
			} else if found, ok := byPath[dependency]; ok {
				target = found
			} else {
				matches := providers[dependency]
				if len(matches) > 1 {
					ids := []string{}
					for _, provider := range matches {
						if id := enabled[provider]; id != "" {
							ids = append(ids, id)
						}
					}
					return fmt.Errorf("repository %s dependency %q has ambiguous meta providers %v (enabled repository IDs %v); declare one provider or depend on an explicit alias", r.ID, dependency, matches, canonical(ids))
				}
				if len(matches) == 1 {
					target = matches[0]
				}
			}
			if target == "" {
				return fmt.Errorf("repository %s dependency %q is unresolved in meta manifest; expected IDs resolved so far: %v", r.ID, dependency, canonical(expected))
			}
			id := enabled[target]
			if id == "" {
				missing = append(missing, fmt.Sprintf("%s (path %s)", target, declarations[target].Path))
				continue
			}
			expected = append(expected, id)
		}
		expected = canonical(expected)
		if len(missing) > 0 {
			return fmt.Errorf("repository %s has missing enabled dependencies: %s; register those meta projects, then set dependsOn to their repository IDs plus %v", r.ID, strings.Join(missing, ", "), expected)
		}
		configured := canonical(r.DependsOn)
		if strings.Join(expected, "\x00") != strings.Join(configured, "\x00") {
			return fmt.Errorf("repository %s dependsOn differs from pinned meta manifest: expected IDs %v, configured IDs %v", r.ID, expected, configured)
		}
	}
	return nil
}
func canonical(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
