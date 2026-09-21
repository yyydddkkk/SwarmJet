package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var IDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

// ValidateProjectForSave accepts incomplete disabled drafts while rejecting unsafe
// supplied values. Enabling a project always requires the execution contract.
func ValidateProjectForSave(p Project) error {
	if !IDPattern.MatchString(p.ID) || strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("project id and name are required; id must be a safe identifier")
	}
	if p.Version < 0 {
		return fmt.Errorf("project version must not be negative")
	}
	if p.MetaMode != "" && p.MetaMode != MetaModeManaged && p.MetaMode != MetaModeImported {
		return fmt.Errorf("unknown metaMode")
	}
	if p.ManagedMeta() {
		// Managed Meta-Repos are created and owned by the system from the
		// selected repositories; accepting a caller URL here would bypass the
		// existing file:// and host validation.
		if p.MetaURL != "" {
			return fmt.Errorf("managed projects must not set metaUrl")
		}
	} else if !safeRepositoryURL(p.MetaURL) {
		return fmt.Errorf("metaUrl must be a valid remote repository URL without credentials, query or fragment")
	} else if p.GitLabURL != "" && !sameGitLabAuthority(p.GitLabURL, p.MetaURL) {
		return fmt.Errorf("metaUrl must use the configured GitLab host")
	}
	if p.ConnectionID != "" && !IDPattern.MatchString(p.ConnectionID) {
		return fmt.Errorf("invalid connection reference")
	}
	if p.GitLabURL != "" {
		u, err := url.Parse(p.GitLabURL)
		if err != nil || !safeRepositoryURL(p.GitLabURL) || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid GitLab URL")
		}
	}
	if p.MetaRef != "" && !ValidBranch(p.MetaRef) {
		return fmt.Errorf("invalid metaRef")
	}
	envRef := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	for _, ref := range []string{p.CredentialRef, p.MergeCredentialRef} {
		if ref != "" && !envRef.MatchString(ref) {
			return fmt.Errorf("credential references must name deployment environment variables")
		}
	}
	seenIDs := map[string]bool{}
	seenPaths := map[string]bool{}
	seenProjects := map[int64]bool{}
	for _, repo := range p.Repositories {
		if repo.ID != "" {
			if !IDPattern.MatchString(repo.ID) || seenIDs[repo.ID] {
				return fmt.Errorf("invalid or duplicate repository id %q", repo.ID)
			}
			seenIDs[repo.ID] = true
		}
		if repo.ProjectID < 0 || repo.ProjectID > 0 && seenProjects[repo.ProjectID] {
			return fmt.Errorf("invalid or duplicate repository projectId")
		}
		if repo.ProjectID > 0 {
			seenProjects[repo.ProjectID] = true
		}
		if repo.URL != "" {
			if !safeRepositoryURL(repo.URL) {
				return fmt.Errorf("invalid repository URL")
			}
			if p.GitLabURL != "" && !sameGitLabAuthority(p.GitLabURL, repo.URL) {
				return fmt.Errorf("repository URL must use the configured GitLab host")
			}
		}
		if repo.Path != "" {
			if !safeRelativePath(repo.Path) || seenPaths[repo.Path] {
				return fmt.Errorf("invalid or duplicate repository path %q", repo.Path)
			}
			seenPaths[repo.Path] = true
		}
		for _, ref := range []string{repo.TargetBranch, repo.CreateFrom} {
			if ref != "" && !ValidBranch(ref) {
				return fmt.Errorf("invalid repository branch or createFrom")
			}
		}
		deps := map[string]bool{}
		for _, dep := range repo.DependsOn {
			if !IDPattern.MatchString(dep) || deps[dep] || dep == repo.ID {
				return fmt.Errorf("invalid or duplicate repository dependency")
			}
			deps[dep] = true
		}
		for _, path := range repo.SensitivePaths {
			if !safeRelativePath(path) {
				return fmt.Errorf("invalid sensitive path")
			}
		}
	}
	for _, agent := range []AgentConfig{p.Development, p.Review} {
		if agent.Adapter != "" && agent.Adapter != "opencode" {
			return fmt.Errorf("unsupported agent adapter; only opencode is available")
		}
		if agent.Model != "" {
			provider, model, ok := strings.Cut(agent.Model, "/")
			if !ok || provider == "" || model == "" || strings.ContainsAny(agent.Model, " \t\r\n\x00") {
				return fmt.Errorf("agent model must use provider/model format")
			}
		}
		if agent.Image != "" && (strings.HasPrefix(agent.Image, "-") || strings.ContainsAny(agent.Image, " \t\r\n\x00")) {
			return fmt.Errorf("invalid agent image")
		}
		if agent.TimeoutSeconds < 0 || agent.TimeoutSeconds > 86400 {
			return fmt.Errorf("agent timeout must be 0..86400 for drafts, 1..86400 when enabled")
		}
	}
	if p.MaxReworks < 0 || p.MaxReworks > 20 {
		return fmt.Errorf("maxReworks must be 0..20")
	}
	if p.Integration.ComposeFile != "" && !safeRelativePath(p.Integration.ComposeFile) {
		return fmt.Errorf("integration composeFile must be a safe relative path")
	}
	if p.Integration.StartupTimeoutSeconds < 0 {
		return fmt.Errorf("integration startup timeout must not be negative")
	}
	checks := map[string]bool{}
	serviceName := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	for _, check := range p.Integration.Checks {
		if check.Name != "" {
			if strings.TrimSpace(check.Name) == "" || strings.ContainsAny(check.Name, "\x00\r\n") || checks[check.Name] {
				return fmt.Errorf("invalid or duplicate check name")
			}
			checks[check.Name] = true
		}
		if check.Service != "" && !serviceName.MatchString(check.Service) {
			return fmt.Errorf("invalid check service")
		}
		for i, arg := range check.Command {
			if strings.ContainsRune(arg, 0) || i == 0 && strings.TrimSpace(arg) == "" {
				return fmt.Errorf("check command must contain valid argv strings")
			}
		}
	}
	if p.Enabled {
		return ValidateProject(p)
	}
	return nil
}

// sameGitLabAuthority reports whether a repository or Meta URL belongs to the
// configured GitLab authority. Only the host is compared, so HTTP/HTTPS and the
// SSH forms of the same instance are accepted; embedded credentials, ports on a
// different host, queries and fragments are rejected.
func sameGitLabAuthority(gitlabURL, candidate string) bool {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(gitlabURL), "/"))
	if err != nil || base.Hostname() == "" {
		return false
	}
	if strings.HasPrefix(candidate, "git@") && !strings.Contains(candidate, "://") {
		host, _, ok := strings.Cut(strings.TrimPrefix(candidate, "git@"), ":")
		return ok && host != "" && strings.EqualFold(host, base.Hostname())
	}
	u, err := url.Parse(candidate)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return strings.EqualFold(u.Hostname(), base.Hostname())
}

func safeRelativePath(path string) bool {
	if path == "" || filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\\\x00\r\n") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." || part == ".git" {
			return false
		}
	}
	return true
}
func safeRepositoryURL(raw string) bool {
	if raw == "" || strings.ContainsAny(raw, " \t\r\n\x00#") {
		return false
	}
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		host, path, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		return ok && host != "" && !strings.ContainsAny(host, "/@?#") && safeRelativePath(path) && !strings.ContainsAny(path, "?#")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ssh" {
		return false
	}
	if u.User != nil {
		if u.Scheme != "ssh" {
			return false
		}
		if _, password := u.User.Password(); password {
			return false
		}
	}
	return true
}

func ValidateProject(p Project) error {
	if !IDPattern.MatchString(p.ID) || strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("project id and name are required; id must be a safe identifier")
	}
	u, e := url.Parse(p.GitLabURL)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return fmt.Errorf("invalid GitLab URL")
	}
	if p.MetaMode != "" && p.MetaMode != MetaModeManaged && p.MetaMode != MetaModeImported {
		return fmt.Errorf("unknown metaMode")
	}
	if p.ManagedMeta() {
		if p.MetaURL != "" {
			return fmt.Errorf("managed projects must not set metaUrl")
		}
	} else if p.MetaURL == "" {
		return fmt.Errorf("metaUrl, metaRef and repositories are required")
	}
	if p.MetaRef == "" || len(p.Repositories) == 0 {
		return fmt.Errorf("metaUrl, metaRef and repositories are required")
	}
	if p.MetaURL != "" && !sameGitLabAuthority(p.GitLabURL, p.MetaURL) {
		return fmt.Errorf("metaUrl must use the configured GitLab host")
	}
	if p.ConnectionID != "" {
		if !IDPattern.MatchString(p.ConnectionID) {
			return fmt.Errorf("invalid connection reference")
		}
	} else if !IDPattern.MatchString(p.CredentialRef) {
		return fmt.Errorf("credential references must name deployment environment variables")
	}
	if p.MergeCredentialRef != "" && !IDPattern.MatchString(p.MergeCredentialRef) {
		return fmt.Errorf("credential references must name deployment environment variables")
	}
	seen := map[string]bool{}
	paths := map[string]bool{}
	ids := map[int64]bool{}
	for _, r := range p.Repositories {
		if !IDPattern.MatchString(r.ID) || seen[r.ID] || ids[r.ProjectID] || r.ProjectID <= 0 {
			return fmt.Errorf("invalid or duplicate repository %q", r.ID)
		}
		seen[r.ID] = true
		ids[r.ProjectID] = true
		if r.URL == "" || !ValidBranch(r.TargetBranch) {
			return fmt.Errorf("repository %s needs URL and valid targetBranch", r.ID)
		}
		if !sameGitLabAuthority(p.GitLabURL, r.URL) {
			return fmt.Errorf("repository %s URL must use the configured GitLab host", r.ID)
		}
		if r.CreateFrom != "" && !ValidBranch(r.CreateFrom) {
			return fmt.Errorf("invalid createFrom")
		}
		if r.Path == "" || filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path || r.Path == ".." || strings.HasPrefix(r.Path, "../") || strings.HasPrefix(r.Path, ".git") || paths[r.Path] {
			return fmt.Errorf("invalid repository path %q", r.Path)
		}
		paths[r.Path] = true
	}
	for _, r := range p.Repositories {
		for _, dep := range r.DependsOn {
			if !seen[dep] {
				return fmt.Errorf("unknown repository dependency %s", dep)
			}
		}
	}
	for _, a := range []AgentConfig{p.Development, p.Review} {
		if a.Adapter != "opencode" || a.Model == "" || a.Image == "" || a.TimeoutSeconds < 1 || a.TimeoutSeconds > 86400 {
			return fmt.Errorf("enabled adapter must be opencode with model, image and timeout 1..86400")
		}
	}
	if p.MaxReworks < 0 || p.MaxReworks > 20 {
		return fmt.Errorf("maxReworks must be 0..20")
	}
	f := p.Integration.ComposeFile
	if f == "" || filepath.IsAbs(f) || filepath.Clean(f) != f || f == ".." || strings.HasPrefix(f, "../") {
		return fmt.Errorf("integration composeFile must be a relative path inside meta repo")
	}
	required := false
	for _, c := range p.Integration.Checks {
		if c.Name == "" || c.Service == "" || len(c.Command) == 0 {
			return fmt.Errorf("checks require name, service, command")
		}
		required = required || c.Required
	}
	if !required {
		return fmt.Errorf("at least one required integration check is needed")
	}
	return nil
}
func ValidBranch(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "/") && !strings.HasSuffix(s, "/") && !strings.HasSuffix(s, ".") && !strings.HasSuffix(s, ".lock") && !strings.ContainsAny(s, " ~^:?*[\\\t\r\n") && !strings.Contains(s, "..") && !strings.Contains(s, "@{") && !strings.Contains(s, "//")
}
func Digest(revisions []Revision) string {
	rows := make([]string, 0, len(revisions))
	for _, r := range revisions {
		rows = append(rows, fmt.Sprintf("%s:%s:%s:%s", r.RepositoryID, r.BaseSHA, r.HeadSHA, r.TargetBranch))
	}
	sort.Strings(rows)
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func MergeOrder(repos []Repository) []string {
	left := map[string]Repository{}
	done := map[string]bool{}
	out := []string{}
	for _, r := range repos {
		left[r.ID] = r
	}
	for len(left) > 0 {
		keys := []string{}
		for k := range left {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		picked := ""
		for _, k := range keys {
			ok := true
			for _, d := range left[k].DependsOn {
				if !done[d] {
					ok = false
				}
			}
			if ok {
				picked = k
				break
			}
		}
		if picked == "" {
			picked = keys[0]
		}
		out = append(out, picked)
		done[picked] = true
		delete(left, picked)
	}
	return out
}
func Terminal(stage string) bool { return stage == "completed" || stage == "cancelled" }

// NormalizeGitLabURL validates and canonicalises an http(s) GitLab base URL.
// The path keeps an optional relative URL root but drops a trailing /api/v4.
func NormalizeGitLabURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("gitlabUrl is required")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", fmt.Errorf("gitlabUrl must be an http(s) URL without credentials, query or fragment")
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/api/v4")
	return u.String(), nil
}

// ValidateConnection checks the non-secret metadata of a saved connection. The
// token is validated separately by performing a real GitLab request.
func ValidateConnection(c Connection) error {
	if !IDPattern.MatchString(c.ID) || strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("connection id and name are required")
	}
	if _, err := NormalizeGitLabURL(c.GitLabURL); err != nil {
		return err
	}
	if c.Version < 0 {
		return fmt.Errorf("connection version must not be negative")
	}
	return nil
}

func GitLabIdentity(raw string) string {
	u, e := url.Parse(strings.TrimRight(raw, "/"))
	if e != nil {
		return strings.TrimRight(raw, "/")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/api/v4")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}
