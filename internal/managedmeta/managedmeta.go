// Package managedmeta creates and versions the internal Meta-Repo that
// SwarmJet owns for a project. It never creates a remote GitLab project and
// never accepts a caller-supplied server path.
package managedmeta

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"swarmjet/internal/domain"
)

type Manager struct {
	DataDir string
}

func New(dataDir string) *Manager {
	abs, _ := filepath.Abs(dataDir)
	return &Manager{DataDir: abs}
}

var idPattern = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$")

// Root is the managed working repository for a project. It is derived only from
// the validated project ID, never from request input.
func (m *Manager) Root(projectID string) (string, error) {
	if !idPattern.MatchString(projectID) {
		return "", errors.New("unsafe project id")
	}
	return filepath.Join(m.DataDir, "managed-meta", projectID, "work"), nil
}

// Manifest renders the deterministic .meta.yaml consumed by Workspace.Prepare.
func Manifest(repositories []domain.Repository) ([]byte, error) {
	sorted := append([]domain.Repository(nil), repositories...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	projects := &yaml.Node{Kind: yaml.MappingNode}
	seen := map[string]bool{}
	for _, r := range sorted {
		if !idPattern.MatchString(r.ID) || r.URL == "" || r.Path == "" {
			return nil, fmt.Errorf("repository %q is not ready for the Meta manifest", r.ID)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("duplicate repository %q", r.ID)
		}
		seen[r.ID] = true
		entry := &yaml.Node{Kind: yaml.MappingNode}
		entry.Content = append(entry.Content, str("repo"), str(r.URL), str("path"), str(r.Path))
		if len(r.DependsOn) > 0 {
			seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
			deps := append([]string(nil), r.DependsOn...)
			sort.Strings(deps)
			for _, d := range deps {
				seq.Content = append(seq.Content, str(d))
			}
			entry.Content = append(entry.Content, str("depends_on"), seq)
		}
		projects.Content = append(projects.Content, str(r.ID), entry)
	}
	root := &yaml.Node{Kind: yaml.MappingNode}
	root.Content = append(root.Content, str("version"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "1"})
	root.Content = append(root.Content, str("projects"), projects)
	return yaml.Marshal(root)
}

func str(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

// Files returns the initial, deterministic contents of a managed Meta-Repo.
func Files(name string, repositories []domain.Repository) (map[string]string, error) {
	manifest, err := Manifest(repositories)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("# " + strings.TrimSpace(name) + "\n\n")
	b.WriteString("本仓库由 SwarmJet 托管，记录参与开发的仓库清单与基础执行约定。\n\n")
	b.WriteString("## 仓库清单\n\n")
	sorted := append([]domain.Repository(nil), repositories...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, r := range sorted {
		fmt.Fprintf(&b, "- %s（目标分支 %s）：%s\n", r.Path, r.TargetBranch, r.URL)
	}
	b.WriteString("\n## 执行约定\n\n")
	b.WriteString("- 每个任务使用私有 worktree，任务分支从各自目标分支创建。\n")
	b.WriteString("- 合并请求只指向本清单记录的目标分支。\n")
	b.WriteString("- 本说明可由初始化分析补充或人工修订；再次分析会形成新的 Meta 提交。\n")
	return map[string]string{".meta.yaml": string(manifest), "README.md": b.String()}, nil
}

func (m *Manager) run(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(m.DataDir, "git-home"),
		"LANG=C",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=file:https:http:ssh",
		"GIT_AUTHOR_NAME=swarmjet",
		"GIT_AUTHOR_EMAIL=swarmjet@localhost",
		"GIT_COMMITTER_NAME=swarmjet",
		"GIT_COMMITTER_EMAIL=swarmjet@localhost",
	}
	output, err := command.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	flags := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}
	return m.run(ctx, dir, append(flags, args...)...)
}

func safeFile(path string) error {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || strings.IndexByte(path, 92) >= 0 || strings.IndexByte(path, ':') >= 0 || strings.IndexByte(path, 0) >= 0 {
		return errors.New("unsafe managed Meta file path")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".git") {
			return errors.New("unsafe managed Meta file path")
		}
	}
	return nil
}

func (m *Manager) write(root string, files map[string]string) error {
	for name, body := range files {
		if err := safeFile(name); err != nil {
			return err
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(body), 0600); err != nil {
			return err
		}
	}
	return nil
}

// Ensure creates the managed repository with the initial commit when absent and
// otherwise returns the existing ref unchanged, preserving manual revisions.
func (m *Manager) Ensure(ctx context.Context, projectID, ref string, files map[string]string) (string, error) {
	if !domain.ValidBranch(ref) {
		return "", errors.New("invalid Meta ref")
	}
	root, err := m.Root(projectID)
	if err != nil {
		return "", err
	}
	if _, err = os.Stat(filepath.Join(root, ".git")); errors.Is(err, os.ErrNotExist) {
		if err = os.MkdirAll(filepath.Dir(root), 0700); err != nil {
			return "", err
		}
		if _, err = m.git(ctx, "", "init", "-q", root); err != nil {
			return "", err
		}
		if err = m.write(root, files); err != nil {
			return "", err
		}
		if _, err = m.git(ctx, root, "add", "-A"); err != nil {
			return "", err
		}
		if _, err = m.git(ctx, root, "commit", "-q", "-m", "Initialize managed Meta-Repo"); err != nil {
			return "", err
		}
		if _, err = m.git(ctx, root, "branch", "-M", ref); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	return m.git(ctx, root, "rev-parse", "--verify", "refs/heads/"+ref)
}

// Commit publishes generated files on ref. It is a no-op when nothing changed,
// so retries never duplicate commits.
func (m *Manager) Commit(ctx context.Context, projectID, ref, message string, files map[string]string) (string, error) {
	if !domain.ValidBranch(ref) {
		return "", errors.New("invalid Meta ref")
	}
	root, err := m.Root(projectID)
	if err != nil {
		return "", err
	}
	if _, err = m.git(ctx, root, "checkout", "-q", ref); err != nil {
		return "", err
	}
	if err = m.write(root, files); err != nil {
		return "", err
	}
	if _, err = m.git(ctx, root, "add", "-A"); err != nil {
		return "", err
	}
	status, err := m.git(ctx, root, "status", "--porcelain", "--", ".")
	if err != nil {
		return "", err
	}
	if status == "" {
		return m.git(ctx, root, "rev-parse", "HEAD")
	}
	if _, err = m.git(ctx, root, "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	return m.git(ctx, root, "rev-parse", "HEAD")
}

// ReadFile returns a committed file at ref for validation and display.
func (m *Manager) ReadFile(ctx context.Context, projectID, ref, name string) ([]byte, error) {
	if err := safeFile(name); err != nil {
		return nil, err
	}
	root, err := m.Root(projectID)
	if err != nil {
		return nil, err
	}
	out, err := m.git(ctx, root, "show", ref+":"+name)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}
