package repochange

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// addManifestEntry edits only the projects mapping and retains other Meta
// content, including comments. It refuses to reinterpret an existing entry.
func addManifestEntry(body []byte, repo domain.Repository, registered []domain.Repository) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, errors.New("Meta 清单不是有效的 YAML 映射")
	}
	root := doc.Content[0]
	var projects *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "projects" {
			projects = root.Content[i+1]
			break
		}
	}
	if projects == nil || projects.Kind != yaml.MappingNode {
		return nil, false, errors.New("Meta 清单缺少 projects 映射")
	}
	aliases := map[string]string{}
	byAlias := map[string]bool{}
	providers := map[string][]string{}
	var matched *yaml.Node
	for i := 0; i+1 < len(projects.Content); i += 2 {
		alias, entry := projects.Content[i].Value, projects.Content[i+1]
		path, url, err := manifestEntry(alias, entry)
		if err != nil {
			return nil, false, err
		}
		if aliases[path] != "" || byAlias[alias] {
			return nil, false, errors.New("Meta 清单包含重复仓库别名或路径")
		}
		aliases[path] = alias
		byAlias[alias] = true
		if entry.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(entry.Content); j += 2 {
				if entry.Content[j].Value == "provides" {
					for _, capability := range entry.Content[j+1].Content {
						providers[capability.Value] = append(providers[capability.Value], alias)
					}
				}
			}
		}
		if alias == repo.ID || path == repo.Path {
			if alias != repo.ID || path != repo.Path || url != repo.URL {
				return nil, false, fmt.Errorf("Meta 清单中的别名或路径 %s 已被其他仓库占用", repo.ID)
			}
			matched = entry
		}
	}
	deps := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, id := range repo.DependsOn {
		var depPath string
		for _, r := range registered {
			if r.ID == id {
				depPath = r.Path
				break
			}
		}
		alias := aliases[depPath]
		if alias == "" {
			return nil, false, fmt.Errorf("依赖仓库 %s 不在 Meta 清单中", id)
		}
		deps.Content = append(deps.Content, scalar(alias))
	}
	if matched != nil {
		actual := []string{}
		if matched.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(matched.Content); i += 2 {
				if matched.Content[i].Value == "depends_on" {
					for _, dep := range matched.Content[i+1].Content {
						resolved := dep.Value
						if !byAlias[resolved] {
							if alias := aliases[resolved]; alias != "" {
								resolved = alias
							} else if len(providers[resolved]) == 1 {
								resolved = providers[resolved][0]
							}
						}
						actual = append(actual, resolved)
					}
				}
			}
		}
		expected := []string{}
		for _, dep := range deps.Content {
			expected = append(expected, dep.Value)
		}
		sort.Strings(actual)
		sort.Strings(expected)
		if strings.Join(actual, "\x00") != strings.Join(expected, "\x00") {
			return nil, false, errors.New("Meta 清单已有该仓库，但依赖关系不同；请按清单选择依赖")
		}
		return body, false, nil
	}
	entry := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{scalar("repo"), scalar(repo.URL), scalar("path"), scalar(repo.Path)}}
	if len(deps.Content) > 0 {
		entry.Content = append(entry.Content, scalar("depends_on"), deps)
	}
	projects.Content = append(projects.Content, scalar(repo.ID), entry)
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&doc); err != nil {
		return nil, false, err
	}
	return out.Bytes(), true, nil
}

func scalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func manifestEntry(alias string, entry *yaml.Node) (string, string, error) {
	if entry.Kind == yaml.ScalarNode && entry.Tag == "!!str" {
		return alias, entry.Value, nil
	}
	if entry.Kind != yaml.MappingNode {
		return "", "", fmt.Errorf("Meta 清单中的 %s 格式无效", alias)
	}
	path, url := alias, ""
	for i := 0; i+1 < len(entry.Content); i += 2 {
		switch entry.Content[i].Value {
		case "path":
			if value := strings.TrimSpace(entry.Content[i+1].Value); value != "" {
				path = value
			}
		case "repo", "url":
			url = entry.Content[i+1].Value
		}
	}
	return path, url, nil
}
