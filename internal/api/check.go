package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"swarmjet/internal/domain"
	"swarmjet/internal/managedmeta"
)

type checkResult struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func (a *server) check(w http.ResponseWriter, r *http.Request, p domain.Project) {
	checks := []checkResult{}
	appendCheck := func(name string, err error, message string) {
		if err != nil {
			message = err.Error()
		}
		checks = append(checks, checkResult{Name: name, OK: err == nil, Message: message})
	}
	if err := domain.ValidateProject(p); err != nil {
		appendCheck("configuration", err, "")
	} else {
		appendCheck("configuration", nil, "configuration valid")
	}
	client, err := a.hostClient(p)
	if err != nil {
		appendCheck("credential", errors.New("hosting credential unavailable"), "")
	} else {
		for _, repo := range p.Repositories {
			_, err := client.GetBranch(r.Context(), repo.ProjectID, repo.TargetBranch)
			if err != nil {
				appendCheck("target:"+repo.ID, errors.New("target branch unavailable; configure createFrom for initialization during task preparation"), "")
			} else {
				appendCheck("target:"+repo.ID, nil, "target branch readable")
			}
		}
		if p.ManagedMeta() {
			appendCheck("meta", a.checkManagedMeta(p), "Managed Meta-Repo manifest and compose file match registered configuration")
		} else {
			token, err := a.projectCredential(p)
			if err != nil {
				appendCheck("meta", err, "")
			} else {
				appendCheck("meta", checkMeta(r.Context(), p, token), "Meta manifest and compose file match registered configuration")
			}
		}
	}
	all := true
	for _, v := range checks {
		all = all && v.OK
	}
	respond(w, 200, map[string]any{"ok": all, "checks": checks})
}

// checkManagedMeta validates a system-managed Meta-Repo from the shared data
// directory. It never needs a remote Meta URL or an internal server path from
// the caller.
func (a *server) checkManagedMeta(p domain.Project) error {
	root, err := managedmeta.New(a.cfg.DataDir).Root(p.ID)
	if err != nil {
		return err
	}
	manifest, err := os.ReadFile(filepath.Join(root, ".meta.yaml"))
	if err != nil {
		return errors.New("managed Meta-Repo is not initialized")
	}
	var parsed struct {
		Projects map[string]any `yaml:"projects"`
	}
	if yaml.Unmarshal(manifest, &parsed) != nil || len(parsed.Projects) == 0 {
		return errors.New("invalid managed Meta manifest")
	}
	entries := map[string]string{}
	for alias, value := range parsed.Projects {
		dest, url := alias, ""
		if v, ok := value.(map[string]any); ok {
			if x, ok := v["path"].(string); ok && x != "" {
				dest = x
			}
			if x, ok := v["repo"].(string); ok {
				url = x
			}
		} else if s, ok := value.(string); ok {
			url = s
		}
		entries[dest] = url
	}
	for _, repo := range p.Repositories {
		if repo.Path == "." || repo.Path == "" {
			continue
		}
		if entries[repo.Path] != repo.URL {
			return fmt.Errorf("managed Meta manifest mismatch for repository %s", repo.ID)
		}
	}
	if p.AcceptanceStrategy == "none" || p.AcceptanceStrategy == "repository" || p.AcceptanceStrategy == "gitlab_ci" || p.AcceptanceStrategy == "platform_ci" || p.Integration.Mode == "commands" {
		return nil
	}
	compose, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(p.Integration.ComposeFile)))
	if err != nil {
		return errors.New("integration compose file unavailable in managed Meta-Repo")
	}
	var composeDoc struct {
		Services map[string]any `yaml:"services"`
	}
	if yaml.Unmarshal(compose, &composeDoc) != nil || len(composeDoc.Services) == 0 {
		return errors.New("invalid integration compose file")
	}
	for _, check := range p.Integration.Checks {
		if _, ok := composeDoc.Services[check.Service]; !ok {
			return fmt.Errorf("compose service missing for check %s", check.Name)
		}
	}
	return nil
}

func metaProjectPath(p domain.Project) (string, error) {
	base, err := url.Parse(p.GitLabURL)
	if err != nil {
		return "", err
	}
	raw := p.MetaURL
	if strings.HasPrefix(raw, "git@") {
		hostPath := strings.TrimPrefix(raw, "git@")
		host, path, ok := strings.Cut(hostPath, ":")
		if !ok || host != base.Hostname() {
			return "", errors.New("Meta URL must use configured GitLab host")
		}
		return strings.TrimSuffix(path, ".git"), nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() != base.Hostname() || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("Meta URL must use configured GitLab host without embedded credentials")
	}
	path := strings.TrimPrefix(u.Path, strings.TrimSuffix(base.Path, "/api/v4"))
	path = strings.Trim(strings.TrimSuffix(path, ".git"), "/")
	if path == "" {
		return "", errors.New("Meta project path required")
	}
	return path, nil
}
func checkMeta(ctx context.Context, p domain.Project, token string) error {
	project, err := metaProjectPath(p)
	if err != nil {
		return err
	}
	base := strings.TrimRight(p.GitLabURL, "/")
	if !strings.HasSuffix(base, "/api/v4") {
		base += "/api/v4"
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(file string) ([]byte, error) {
		endpoint := base + "/projects/" + url.PathEscape(project) + "/repository/files/" + url.PathEscape(file) + "/raw?ref=" + url.QueryEscape(p.MetaRef)
		req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			return nil, errors.New("invalid Meta API request")
		}
		req.Header.Set("PRIVATE-TOKEN", token)
		resp, err := client.Do(req)
		if err != nil {
			return nil, errors.New("unable to read Meta repository")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("Meta file read returned HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			return nil, errors.New("unable to read bounded Meta file")
		}
		return data, nil
	}
	var data []byte
	for _, name := range []string{".meta.yaml", ".meta.yml", ".meta"} {
		data, err = get(name)
		if err == nil {
			break
		}
	}
	if err != nil {
		return errors.New("Meta ref has no readable manifest")
	}
	var manifest struct {
		Projects map[string]any `yaml:"projects"`
	}
	if yaml.Unmarshal(data, &manifest) != nil || len(manifest.Projects) == 0 {
		return errors.New("invalid Meta manifest")
	}
	entries := map[string]string{}
	for alias, value := range manifest.Projects {
		switch v := value.(type) {
		case string:
			entries[alias] = v
		case map[string]any:
			path, _ := v["path"].(string)
			if path == "" {
				path = alias
			}
			repo, _ := v["url"].(string)
			if repo == "" {
				repo, _ = v["repo"].(string)
			}
			entries[path] = repo
		default:
			return errors.New("invalid Meta project entry")
		}
	}
	for _, repo := range p.Repositories {
		if repo.Path == "." {
			if repo.URL != p.MetaURL {
				return errors.New("root repository must match Meta URL")
			}
		} else if entries[repo.Path] != repo.URL {
			return fmt.Errorf("Meta manifest mismatch for repository %s", repo.ID)
		}
	}
	if p.AcceptanceStrategy == "none" || p.AcceptanceStrategy == "repository" || p.AcceptanceStrategy == "gitlab_ci" || p.AcceptanceStrategy == "platform_ci" || p.Integration.Mode == "commands" {
		return nil
	}
	compose, err := get(p.Integration.ComposeFile)
	if err != nil {
		return errors.New("integration compose file unavailable at Meta ref")
	}
	var parsed struct {
		Services map[string]any `yaml:"services"`
	}
	if yaml.Unmarshal(compose, &parsed) != nil || len(parsed.Services) == 0 {
		return errors.New("invalid integration compose file")
	}
	for _, check := range p.Integration.Checks {
		if _, ok := parsed.Services[check.Service]; !ok {
			return fmt.Errorf("compose service missing for check %s", check.Name)
		}
	}
	return nil
}
