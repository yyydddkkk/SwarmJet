// Package workspace manages durable, isolated task worktrees without resetting task changes.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"swarmjet/internal/domain"
	"swarmjet/internal/managedmeta"
	"syscall"
)

type Manager struct {
	DataDir, MetaCommand string
	Env                  map[string]string
}

func New(dataDir, metaCommand string) *Manager {
	abs, _ := filepath.Abs(dataDir)
	return &Manager{DataDir: abs, MetaCommand: metaCommand}
}

type state struct {
	MetaStarted bool              `json:"metaStarted,omitempty"`
	Source      string            `json:"source"`
	Released    bool              `json:"released,omitempty"`
	Workspace   domain.Workspace  `json:"workspace"`
	Project     domain.Project    `json:"project"`
	MetaBranch  string            `json:"metaBranch"`
	MetaHead    string            `json:"metaHead"`
	Aliases     map[string]string `json:"aliases"`
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func validID(s string) error {
	if !safeID.MatchString(s) {
		return fmt.Errorf("unsafe identifier %q", s)
	}
	return nil
}
func validPath(s string) error {
	if s == "" || s == "." || filepath.IsAbs(s) || filepath.Clean(s) != s || s == ".." || strings.HasPrefix(s, "../") || strings.ContainsAny(s, "\\:\x00") || strings.HasPrefix(s, ".git") || strings.HasPrefix(s, ".worktrees") {
		return fmt.Errorf("unsafe repository path %q", s)
	}
	return nil
}
func (m *Manager) run(ctx context.Context, dir, exe string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, exe, args...)
	c.Dir = dir
	// Do not leak worker/API/merge secrets into subprocesses. Only explicitly
	// injected read credentials and the process search path are inherited.
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(m.DataDir, "git-home"), "LANG=C", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:http:ssh:file"}
	for k, v := range m.Env {
		c.Env = append(c.Env, k+"="+v)
	}
	b, e := c.CombinedOutput()
	if e != nil {
		text := strings.TrimSpace(string(b))
		for k, v := range m.Env {
			if v != "" && (strings.Contains(k, "TOKEN") || strings.Contains(k, "PASSWORD") || strings.Contains(k, "SECRET")) {
				text = strings.ReplaceAll(text, v, "[REDACTED]")
			}
		}
		return "", fmt.Errorf("%s operation failed: %w: %s", exe, e, text)
	}
	return strings.TrimSpace(string(b)), nil
}
func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	if err := m.checkPrivateMetadata(ctx, dir); err != nil {
		return "", err
	}
	flags := []string{"-c", "core.askpass=", "-c", "http.sslVerify=true", "-c", "http.proxy=", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "core.sshCommand=ssh -F /dev/null", "-c", "core.gitProxy=", "-c", "diff.external=", "-c", "protocol.ext.allow=never", "-c", "fetch.recurseSubmodules=false", "-c", "submodule.recurse=false"}
	// No host operation may run repository-configured content filters. Git
	// config itself only parses data; command-line overrides disable each filter.
	if dir != "" {
		flags = append(flags, "-c", "core.worktree="+dir)
		cfg, e := m.run(ctx, dir, "git", "config", "--no-includes", "--name-only", "--get-regexp", `^filter\..*\.(process|smudge|clean)$`)
		if e == nil {
			for _, key := range strings.Split(cfg, "\n") {
				if key != "" {
					flags = append(flags, "-c", key+"=")
				}
			}
		}
	}
	return m.run(ctx, dir, "git", append(flags, args...)...)
}

// Validate indirection before invoking host Git against agent-writable metadata.
func (m *Manager) checkPrivateMetadata(ctx context.Context, dir string) error {
	sourceRoot := filepath.Join(m.DataDir, "task-sources")
	rel, e := filepath.Rel(sourceRoot, dir)
	if e != nil || rel == ".." || strings.HasPrefix(rel, "../") || dir == "" {
		return nil
	}
	taskRoot := filepath.Join(sourceRoot, strings.Split(rel, string(filepath.Separator))[0])
	contained := func(p string) bool {
		r, e := filepath.Rel(taskRoot, p)
		return e == nil && r != ".." && !strings.HasPrefix(r, "../")
	}
	if e = noSymlinks(dir); e != nil {
		return e
	}
	gitdir := filepath.Join(dir, ".git")
	st, e := os.Lstat(gitdir)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return errors.New("symlink Git metadata forbidden")
	}
	if !st.IsDir() {
		b, e := os.ReadFile(gitdir)
		if e != nil {
			return e
		}
		v := strings.TrimSpace(strings.TrimPrefix(string(b), "gitdir:"))
		if !filepath.IsAbs(v) {
			v = filepath.Join(dir, v)
		}
		gitdir = filepath.Clean(v)
	}
	if !contained(gitdir) {
		return errors.New("Git metadata escapes private task sources")
	}
	if e = noSymlinks(gitdir); e != nil {
		return e
	}
	common := gitdir
	if b, e := os.ReadFile(filepath.Join(gitdir, "commondir")); e == nil {
		common = strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitdir, common)
		}
		common = filepath.Clean(common)
	}
	if !contained(common) {
		return errors.New("Git common directory escapes private task sources")
	}
	if e = noSymlinks(common); e != nil {
		return e
	}

	// Git writes administrative files such as FETCH_HEAD and index on the host.
	// Reject symlinks anywhere in private Git metadata before those writes.
	for _, base := range []string{gitdir, common} {
		if e = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink in private Git metadata: %s", p)
			}
			return nil
		}); e != nil {
			return e
		}
	}
	for _, base := range []string{gitdir, common} {
		for _, name := range []string{"config", "config.worktree", "objects", "refs"} {
			if e = noSymlinks(filepath.Join(base, name)); e != nil {
				return e
			}
		}
	}
	if b, e := os.ReadFile(filepath.Join(common, "objects", "info", "alternates")); e == nil && strings.TrimSpace(string(b)) != "" {
		return errors.New("shared object alternates forbidden in task sources")
	}
	for _, cfg := range []string{filepath.Join(common, "config"), filepath.Join(gitdir, "config.worktree")} {
		if _, e = os.Stat(cfg); e != nil {
			continue
		}
		keys, e := m.run(ctx, dir, "git", "config", "--no-includes", "--file", cfg, "--name-only", "--get-regexp", `^(include|includeif)\.`)
		if e == nil && keys != "" {
			return errors.New("Git config includes forbidden in private task sources")
		}
	}
	return nil
}
func validURL(url string) error {
	if url == "" || strings.ContainsAny(url, "\n\r\x00") || strings.HasPrefix(url, "-") {
		return fmt.Errorf("unsafe repository URL")
	}
	if filepath.IsAbs(url) || strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "ssh://") || strings.HasPrefix(url, "file://") || regexp.MustCompile(`^[A-Za-z0-9_.-]+@[A-Za-z0-9_.-]+:[^ ]+$`).MatchString(url) {
		return nil
	}
	return errors.New("unsupported repository URL protocol")
}

// metaURL resolves the Meta-Repo location. Managed repositories are derived
// from the validated project ID and are never a caller-supplied path, so the
// existing file:// restriction for user input is not bypassed here.
func (m *Manager) metaURL(p domain.Project) (string, error) {
	if p.ManagedMeta() {
		root, err := managedmeta.New(m.DataDir).Root(p.ID)
		if err != nil {
			return "", err
		}
		if _, err = os.Stat(filepath.Join(root, ".git")); err != nil {
			return "", errors.New("managed Meta-Repo is not initialized")
		}
		return root, nil
	}
	return p.MetaURL, nil
}

func (m *Manager) taskSource(task string) string {
	return filepath.Join(m.DataDir, "task-sources", task, "meta")
}
func (m *Manager) privateClone(ctx context.Context, cache, dest, url string) error {
	if e := noSymlinks(dest); e != nil {
		return e
	}
	if _, e := os.Stat(filepath.Join(dest, ".git")); e == nil {
		u, e := m.git(ctx, dest, "remote", "get-url", "origin")
		if e != nil {
			return e
		}
		if u != url {
			return errors.New("private repository origin mismatch")
		}
		return nil
	}
	if e := os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return e
	}
	tmp, e := os.MkdirTemp(filepath.Dir(dest), ".clone-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	if _, e = m.git(ctx, "", "clone", "--no-checkout", "--no-hardlinks", "--", cache, tmp); e != nil {
		return e
	}
	if _, e = m.git(ctx, tmp, "remote", "set-url", "origin", url); e != nil {
		return e
	}
	return os.Rename(tmp, dest)
}
func (m *Manager) lock(projectID string) (func(), error) {
	if e := validID(projectID); e != nil {
		return nil, e
	}
	d := filepath.Join(m.DataDir, "projects", projectID)
	if e := os.MkdirAll(d, 0700); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(d, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); e != nil {
		f.Close()
		return nil, e
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
func (m *Manager) statePath(taskID string) string {
	return filepath.Join(m.DataDir, "runs", taskID, "workspace.json")
}
func (m *Manager) save(s state) error {
	p := m.statePath(s.Workspace.TaskID)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(p), ".workspace-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(f.Name(), p)
}
func (m *Manager) load(taskID string) (state, error) {
	var s state
	if e := validID(taskID); e != nil {
		return s, e
	}
	b, e := os.ReadFile(m.statePath(taskID))
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(b, &s)
	return s, e
}
func noSymlinks(dir string) error {
	for p := dir; p != filepath.Dir(p); p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink forbidden in workspace path: %s", p)
		}
	}
	return nil
}
func (m *Manager) clone(ctx context.Context, url, dir string) error {
	if err := validURL(url); err != nil {
		return err
	}
	if err := noSymlinks(dir); err != nil {
		return err
	}
	if _, e := os.Stat(filepath.Join(dir, ".git")); e == nil {
		u, e := m.git(ctx, dir, "remote", "get-url", "origin")
		if e != nil {
			return e
		}
		if u != url {
			return fmt.Errorf("repository origin mismatch at %s", dir)
		}
		return nil
	}
	if e := os.MkdirAll(filepath.Dir(dir), 0700); e != nil {
		return e
	}
	_, e := m.git(ctx, "", "clone", "--no-checkout", "--", url, dir)
	return e
}
func (m *Manager) fetch(ctx context.Context, dir, url, ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", errors.New("explicit non-empty git ref required")
	}
	if e := validURL(url); e != nil {
		return "", e
	}
	actual, e := m.git(ctx, dir, "remote", "get-url", "origin")
	if e != nil {
		return "", e
	}
	if actual != url {
		return "", errors.New("repository origin differs from registered URL")
	}
	if _, e := m.git(ctx, dir, "fetch", "--no-recurse-submodules", "--", url, ref); e != nil {
		return "", e
	}
	return m.git(ctx, dir, "rev-parse", "FETCH_HEAD^{commit}")
}

// Manifest URLs and paths must exactly match registered repositories; configuration owns target branches.
func manifest(data []byte) (map[string]string, error) {
	var raw struct {
		Projects map[string]any `yaml:"projects"`
	}
	if e := yaml.Unmarshal(data, &raw); e != nil {
		return nil, e
	}
	out := map[string]string{}
	for p, v := range raw.Projects {
		switch x := v.(type) {
		case string:
			out[p] = x
		case map[string]any:
			u, _ := x["url"].(string)
			if u == "" {
				u, _ = x["repo"].(string)
			}
			dest, _ := x["path"].(string)
			if dest == "" {
				dest = p
			}
			if err := validPath(dest); err != nil {
				return nil, err
			}
			out[dest] = u
		default:
			return nil, fmt.Errorf("invalid manifest entry %s", p)
		}
	}
	return out, nil
}
func (m *Manager) Prepare(ctx context.Context, taskID string, p domain.Project) (domain.Workspace, error) {
	var empty domain.Workspace
	if e := validID(taskID); e != nil {
		return empty, e
	}
	unlock, e := m.lock(p.ID)
	if e != nil {
		return empty, e
	}
	defer unlock()
	s, e := m.load(taskID)
	fresh := errors.Is(e, os.ErrNotExist)
	if e != nil && !fresh {
		return empty, e
	}
	primary := filepath.Join(m.DataDir, "projects", p.ID, "meta")
	metaURL, metaErr := m.metaURL(p)
	if metaErr != nil {
		return empty, metaErr
	}
	if fresh {
		seen := map[string]bool{}
		for _, r := range p.Repositories {
			if e = validID(r.ID); e != nil {
				return empty, e
			}
			if e = validPath(r.Path); r.Path != "." && e != nil {
				return empty, e
			}
			if seen[r.Path] || seen["id:"+r.ID] {
				return empty, errors.New("duplicate repository path or ID")
			}
			for q := range seen {
				if strings.HasPrefix(r.Path, q+"/") || strings.HasPrefix(q, r.Path+"/") {
					return empty, errors.New("overlapping repository paths")
				}
			}
			seen[r.Path] = true
			seen["id:"+r.ID] = true
		}
		if e = m.clone(ctx, metaURL, primary); e != nil {
			return empty, e
		}
		sha, err := m.fetch(ctx, primary, metaURL, p.MetaRef)
		if err != nil {
			return empty, err
		}
		var data string
		for _, name := range []string{".meta.yaml", ".meta.yml", ".meta"} {
			data, err = m.git(ctx, primary, "show", sha+":"+name)
			if err == nil {
				break
			}
		}
		if err != nil {
			return empty, errors.New("meta ref has no manifest")
		}
		if err = validateManifestDependencies([]byte(data), p.Repositories); err != nil {
			return empty, err
		}
		entries, err := manifest([]byte(data))
		if err != nil {
			return empty, err
		}

		var raw struct {
			Projects map[string]any `yaml:"projects"`
		}
		if err = yaml.Unmarshal([]byte(data), &raw); err != nil {
			return empty, err
		}
		aliases := map[string]string{}
		for alias, value := range raw.Projects {
			dest := alias
			if v, ok := value.(map[string]any); ok {
				if x, ok := v["path"].(string); ok && x != "" {
					dest = x
				}
			}
			if err = validPath(alias); err != nil {
				return empty, fmt.Errorf("unsafe meta alias: %w", err)
			}
			aliases[dest] = alias
		}
		s = state{Source: m.taskSource(taskID), Aliases: aliases, Project: p, MetaBranch: "bot/" + taskID, MetaHead: sha, Workspace: domain.Workspace{TaskID: taskID, Root: filepath.Join(m.taskSource(taskID), ".worktrees", taskID), MetaSHA: sha}}
		for _, r := range p.Repositories {
			if (r.Path != "." && entries[r.Path] != r.URL) || (r.Path == "." && r.URL != metaURL) {
				return empty, fmt.Errorf("manifest URL/path mismatch for %s", r.ID)
			}
			dir := filepath.Join(primary, r.Path)
			if e = m.clone(ctx, r.URL, dir); e != nil {
				return empty, e
			}
			base, err := m.fetch(ctx, dir, r.URL, r.TargetBranch)
			if err != nil {
				return empty, err
			}
			if r.Path == "." && base != sha {
				return empty, errors.New("registered root target must resolve to configured MetaRef")
			}
			s.Workspace.Revisions = append(s.Workspace.Revisions, domain.Revision{RepositoryID: r.ID, ProjectID: r.ProjectID, Path: r.Path, InitialSHA: base, BaseSHA: base, HeadSHA: base, TargetBranch: r.TargetBranch, Branch: s.MetaBranch})
		}
		if e = m.save(s); e != nil {
			return empty, e
		}
	} else {
		if s.Project.ID != p.ID {
			return empty, errors.New("task already belongs to another project")
		}
		p = s.Project
		if s.Released {
			return s.Workspace, errors.New("task workspace has been released")
		}
	}

	if s.Source != m.taskSource(taskID) {
		return s.Workspace, errors.New("legacy shared workspace requires migration to private task sources")
	}
	cache := primary
	if e = m.privateClone(ctx, cache, s.Source, metaURL); e != nil {
		return s.Workspace, e
	}
	for _, r := range p.Repositories {
		if r.Path != "." {
			if e = m.privateClone(ctx, filepath.Join(cache, r.Path), filepath.Join(s.Source, r.Path), r.URL); e != nil {
				return s.Workspace, e
			}
		}
	}
	primary = s.Source
	// Persist all starting SHAs before creating any branch so interruption cannot move a task baseline.
	all := append([]domain.Revision{{Path: ".", Branch: s.MetaBranch, BaseSHA: s.Workspace.MetaSHA}}, s.Workspace.Revisions...)
	for _, r := range all {
		dir := filepath.Join(primary, r.Path)
		if _, e = m.git(ctx, dir, "show-ref", "--verify", "refs/heads/"+r.Branch); e != nil {
			if _, e = m.git(ctx, dir, "branch", r.Branch, r.BaseSHA); e != nil {
				return s.Workspace, e
			}
		}
	}
	if !s.MetaStarted && m.MetaCommand != "" { // meta reads the primary checkout manifest; create it at the pinned root ref first.
		if _, e = m.git(ctx, primary, "checkout", "--detach", s.Workspace.MetaSHA); e != nil {
			return s.Workspace, e
		}
		args := []string{"git", "worktree", "create", taskID, "--repo", ".:" + s.MetaBranch}
		for _, r := range p.Repositories {
			if r.Path != "." {
				alias := s.Aliases[r.Path]
				if alias == "" {
					alias = r.Path
				}
				args = append(args, "--repo", alias+":"+s.MetaBranch)
			}
		}
		args = append(args, "--no-deps", "--strict", "--json")
		_, metaErr := m.run(ctx, primary, m.MetaCommand, args...)
		created := false
		for _, r := range all {
			location, err := m.branchWorktree(ctx, filepath.Join(primary, r.Path), r.Branch)
			if err != nil {
				return s.Workspace, err
			}
			if location != "" && inside(s.Workspace.Root, location) {
				created = true
			}
		}
		if !created {
			if metaErr != nil {
				return s.Workspace, fmt.Errorf("meta creation failed before any worktree was created: %w", metaErr)
			}
			return s.Workspace, errors.New("meta reported success without creating task worktrees")
		}
		s.MetaStarted = true
		if e = m.save(s); e != nil {
			return s.Workspace, e
		}
	}
	for _, r := range all {
		dir := filepath.Join(primary, r.Path)
		dest := filepath.Join(s.Workspace.Root, r.Path)
		if e = noSymlinks(dest); e != nil {
			return s.Workspace, e
		}
		if _, e = os.Stat(filepath.Join(dest, ".git")); errors.Is(e, os.ErrNotExist) {
			existing, err := m.branchWorktree(ctx, dir, r.Branch)
			if err != nil {
				return s.Workspace, err
			}
			if existing != "" {
				if !inside(s.Workspace.Root, existing) {
					return s.Workspace, errors.New("task branch checked out outside its workspace")
				}
				if err = noSymlinks(existing); err != nil {
					return s.Workspace, err
				}
				if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
					return s.Workspace, err
				}
				if _, err = m.git(ctx, dir, "worktree", "move", existing, dest); err != nil {
					return s.Workspace, err
				}
			} else if _, e = m.git(ctx, dir, "worktree", "add", dest, r.Branch); e != nil {
				return s.Workspace, e
			}
		} else if e != nil {
			return s.Workspace, e
		}
		if _, err := m.git(ctx, dest, "merge-base", "--is-ancestor", r.BaseSHA, "HEAD"); err != nil {
			return s.Workspace, fmt.Errorf("task branch no longer descends from recorded baseline: %w", err)
		}
		branch, err := m.git(ctx, dest, "symbolic-ref", "--short", "HEAD")
		if err != nil {
			return s.Workspace, err
		}
		if branch != r.Branch {
			return s.Workspace, fmt.Errorf("unexpected branch at %s: %s", dest, branch)
		}
	}
	return s.Workspace, nil
}
func inside(root, p string) bool {
	r, e := filepath.Rel(root, p)
	return e == nil && r != ".." && !strings.HasPrefix(r, "../")
}
func (m *Manager) branchWorktree(ctx context.Context, dir, branch string) (string, error) {
	out, e := m.git(ctx, dir, "worktree", "list", "--porcelain", "-z")
	if e != nil {
		return "", e
	}
	location := ""
	for _, field := range strings.Split(out, "\x00") {
		if strings.HasPrefix(field, "worktree ") {
			location = strings.TrimPrefix(field, "worktree ")
		}
		if field == "branch refs/heads/"+branch {
			return location, nil
		}
	}
	return "", nil
}
func sensitive(file string, extra []string) bool {
	for _, part := range strings.Split(strings.ToLower(file), "/") {
		switch part {
		case "migration", "migrations", "alembic", "deploy", "deployment", "ci", ".github", ".gitlab", ".circleci", "k8s", "kubernetes", "helm", "terraform":
			return true
		}
	}
	base := strings.ToLower(path.Base(file))
	if strings.HasPrefix(base, ".gitlab-ci") || base == ".auto-devops.yml" || base == "jenkinsfile" || strings.HasPrefix(base, "dockerfile") || strings.HasPrefix(base, "docker-compose") || base == "compose.yaml" || base == "compose.yml" || (strings.HasPrefix(base, "compose.") && (strings.HasSuffix(base, ".yaml") || strings.HasSuffix(base, ".yml"))) || strings.HasSuffix(base, ".tf") || strings.HasSuffix(base, ".tfvars") {
		return true
	}
	for _, g := range extra {
		if file == g || strings.HasPrefix(file, strings.TrimSuffix(g, "/**")+"/") {
			return true
		}
		if ok, _ := path.Match(g, file); ok {
			return true
		}
	}
	return false
}
func (m *Manager) Inspect(ctx context.Context, ws domain.Workspace, p domain.Project) (domain.Workspace, error) {
	s, e := m.load(ws.TaskID)
	if e != nil {
		return ws, e
	}
	unlock, e := m.lock(s.Project.ID)
	if e != nil {
		return ws, e
	}
	defer unlock()
	if ws.Root != s.Workspace.Root {
		return ws, errors.New("workspace root differs from recorded task")
	}
	p = s.Project
	rootRegistered := false
	for _, r := range s.Workspace.Revisions {
		if r.Path == "." {
			rootRegistered = true
		}
	}
	branch, e := m.git(ctx, ws.Root, "symbolic-ref", "--short", "HEAD")
	if e != nil {
		return ws, e
	}
	if branch != s.MetaBranch {
		return ws, errors.New("unexpected meta root branch")
	}
	head, e := m.git(ctx, ws.Root, "rev-parse", "HEAD")
	if e != nil {
		return ws, e
	}
	if !rootRegistered && head != s.Workspace.MetaSHA {
		return ws, errors.New("unregistered meta root has committed changes")
	}
	s.MetaHead = head
	for i, r := range s.Workspace.Revisions {
		mergedHead := r.HeadSHA
		for _, supplied := range ws.Revisions {
			if supplied.RepositoryID == r.RepositoryID {
				r.Merged = supplied.Merged
				if supplied.HeadSHA != "" {
					mergedHead = supplied.HeadSHA
				}
				break
			}
		}
		dir := filepath.Join(ws.Root, r.Path)
		branch, e = m.git(ctx, dir, "symbolic-ref", "--short", "HEAD")
		if e != nil {
			return ws, e
		}
		if branch != r.Branch {
			return ws, fmt.Errorf("unexpected branch in %s", r.RepositoryID)
		}
		statusArgs := []string{"status", "--porcelain", "--untracked-files=all", "--", "."}
		if r.Path == "." {
			for _, child := range p.Repositories {
				if child.Path != "." {
					statusArgs = append(statusArgs, ":(exclude)"+child.Path)
				}
			}
		}
		status, err := m.git(ctx, dir, statusArgs...)
		if err != nil {
			return ws, err
		}
		if status != "" {
			return ws, fmt.Errorf("repository %s has uncommitted changes", r.RepositoryID)
		}
		r.HeadSHA, e = m.git(ctx, dir, "rev-parse", "HEAD")
		if e != nil {
			return ws, e
		}
		if r.Merged && r.HeadSHA != mergedHead {
			return ws, fmt.Errorf("merged repository %s was modified", r.RepositoryID)
		}
		files, err := m.git(ctx, dir, "diff", "--no-ext-diff", "--no-textconv", "--name-only", "-z", r.BaseSHA, r.HeadSHA, "--")
		if err != nil {
			return ws, err
		}
		r.Files = nil
		if files != "" {
			r.Files = strings.Split(strings.TrimSuffix(files, "\x00"), "\x00")
		}
		r.Changed = len(r.Files) > 0
		r.Sensitive = false
		var patterns []string
		for _, repo := range p.Repositories {
			if repo.ID == r.RepositoryID {
				patterns = repo.SensitivePaths
			}
		}
		for _, f := range r.Files {
			r.Sensitive = r.Sensitive || sensitive(f, patterns)
		}
		for _, supplied := range ws.Revisions {
			if supplied.RepositoryID == r.RepositoryID {
				r.MRIID = supplied.MRIID
				r.MRURL = supplied.MRURL
				r.Merged = supplied.Merged
				break
			}
		}
		s.Workspace.Revisions[i] = r
	}
	// Child worktrees are expected untracked entries in the root checkout. Exclude only registered child paths.
	args := []string{"status", "--porcelain", "--untracked-files=all", "--", "."}
	for _, r := range p.Repositories {
		if r.Path != "." {
			args = append(args, ":(exclude)"+r.Path)
		}
	}
	status, e := m.git(ctx, ws.Root, args...)
	if e != nil {
		return ws, e
	}
	if status != "" {
		return ws, errors.New("meta root has uncommitted changes")
	}
	if e = m.save(s); e != nil {
		return ws, e
	}
	return s.Workspace, nil
}
func (m *Manager) Release(ctx context.Context, ws domain.Workspace) error {
	s, e := m.load(ws.TaskID)
	if e != nil {
		return e
	}
	unlock, e := m.lock(s.Project.ID)
	if e != nil {
		return e
	}
	defer unlock()
	if ws.Root != s.Workspace.Root {
		return errors.New("workspace root differs from recorded task")
	}
	if s.Released {
		return nil
	}
	primary := s.Source
	all := []domain.Revision{}
	for _, r := range s.Workspace.Revisions {
		if r.Path != "." {
			all = append(all, r)
		}
	}
	all = append(all, domain.Revision{Path: ".", Branch: s.MetaBranch})
	for _, r := range all {
		dir := filepath.Join(primary, r.Path)
		dest := filepath.Join(ws.Root, r.Path)
		if _, e = os.Stat(filepath.Join(dest, ".git")); e == nil {
			if _, e = m.git(ctx, dir, "worktree", "remove", dest); e != nil {
				return e
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if _, e = m.git(ctx, dir, "show-ref", "--verify", "refs/heads/"+r.Branch); e == nil {
			if _, e = m.git(ctx, dir, "branch", "-d", r.Branch); e != nil {
				// Keep unmerged commits reachable; remote squash merges need not be
				// ancestors of the primary checkout. Retention is not a cleanup failure.
				continue
			}
		}
	}
	s.Released = true
	return m.save(s)
}

// ErrNeedsSync means the task must incorporate the current target before verification.
var ErrNeedsSync = errors.New("task branch needs target synchronization")

// RefreshTargets advances comparison baselines only when each fetched target is
// already contained in its task branch. Original starting points remain immutable.
func (m *Manager) RefreshTargets(ctx context.Context, ws domain.Workspace, p domain.Project) (domain.Workspace, error) {
	s, err := m.load(ws.TaskID)
	if err != nil {
		return ws, err
	}
	unlock, err := m.lock(s.Project.ID)
	if err != nil {
		return ws, err
	}
	defer unlock()
	if ws.Root != s.Workspace.Root || p.ID != s.Project.ID {
		return ws, errors.New("workspace/project differs from recorded task")
	}
	if s.Released {
		return ws, errors.New("task workspace has been released")
	}
	primary := s.Source
	for i, r := range s.Workspace.Revisions {
		for _, supplied := range ws.Revisions {
			if supplied.RepositoryID == r.RepositoryID {
				r.MRIID = supplied.MRIID
				r.MRURL = supplied.MRURL
				r.Merged = supplied.Merged
				break
			}
		}
		if r.Merged {
			s.Workspace.Revisions[i] = r
			continue
		}
		if r.InitialSHA == "" {
			r.InitialSHA = r.BaseSHA
		}
		url, metaErr := m.metaURL(s.Project)
		if metaErr != nil {
			return ws, metaErr
		}
		for _, repo := range s.Project.Repositories {
			if repo.ID == r.RepositoryID {
				url = repo.URL
				break
			}
		}
		target, err := m.fetch(ctx, filepath.Join(primary, r.Path), url, r.TargetBranch)
		if err != nil {
			return ws, err
		}
		if _, err = m.git(ctx, filepath.Join(ws.Root, r.Path), "merge-base", "--is-ancestor", target, "HEAD"); err != nil {
			return ws, fmt.Errorf("%w: repository %s target %s advanced to %s", ErrNeedsSync, r.RepositoryID, r.TargetBranch, target)
		}
		r.BaseSHA = target
		for _, supplied := range ws.Revisions {
			if supplied.RepositoryID == r.RepositoryID {
				r.MRIID = supplied.MRIID
				r.MRURL = supplied.MRURL
				r.Merged = supplied.Merged
				break
			}
		}
		s.Workspace.Revisions[i] = r
	}
	if err = m.save(s); err != nil {
		return ws, err
	}
	return s.Workspace, nil
}
