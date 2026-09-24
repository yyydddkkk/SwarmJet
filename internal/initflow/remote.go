package initflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"swarmjet/internal/domain"
	"swarmjet/internal/execution"
)

// AnalyzeAtNode reuses the restricted static analysis preparation and artifact
// validation; only generated text returns to the control plane.
func AnalyzeAtNode(ctx context.Context, data string, backend execution.AgentBackend, p execution.AnalysisParams, local *Manager) (execution.AnalysisResult, error) {
	var out execution.AnalysisResult
	if !domain.IDPattern.MatchString(p.Project.ID) || p.Initialization.ProjectID != p.Project.ID {
		return out, fmt.Errorf("初始化项目无效")
	}
	m := local
	if m == nil {
		m = &Manager{DataDir: data, Agents: backend, CredentialFor: func(domain.Project) (string, error) {
			if p.Project.HostingProvider() == domain.ProviderGitHub {
				return p.Env["GITHUB_TOKEN"], nil
			}
			return p.Env["GITLAB_TOKEN"], nil
		}, AnalysisEnv: map[string]string{}}
		for k, v := range p.Env {
			if strings.HasSuffix(strings.ToUpper(k), "_PROXY") {
				m.AnalysisEnv[k] = v
			}
		}
	}
	root, e := m.prepareAnalysis(ctx, p.Initialization, p.Project)
	if e != nil {
		return out, e
	}
	generated := filepath.Join(m.initDir(p.Project.ID), "generated")
	if e = os.MkdirAll(generated, 0700); e != nil {
		return out, e
	}
	cfg := p.Project.Development
	if p.Initialization.NodeConfigVersion > 0 && p.Initialization.ExecutionConfig.Image != "" {
		cfg = p.Initialization.ExecutionConfig
	}
	cfg.Adapter = "opencode"
	if p.Initialization.Image != "" {
		cfg.Image = p.Initialization.Image
	}
	vars := map[string]string{}
	for k, v := range m.AnalysisEnv {
		if hostingCredential(k) {
			continue
		}
		vars[k] = v
	}
	ex, e := backend.Start(ctx, domain.AgentRequest{ExecutionID: p.Initialization.ExecutionID, TaskID: p.Project.ID, Role: "init", Workspace: root, GeneratedDir: generated, Prompt: initialPrompt(p.Project, p.Initialization), SessionID: p.Initialization.SessionID, Config: cfg, Env: vars})
	out.Execution = ex
	if e != nil {
		return out, e
	}
	if ex.Status == "succeeded" {
		out.Reports, out.Files, e = collectGenerated(generated)
		if e == nil {
			e = validateGeneratedFiles(out.Files)
		}
	}
	return out, e
}
func validateGeneratedFiles(files map[string]string) error {
	total := 0
	for name, body := range files {
		if filepath.IsAbs(name) || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") || reservedGenerated(name) || len(body) > maxGeneratedFile {
			return fmt.Errorf("生成文件不符合初始化目录约束")
		}
		total += len(body)
	}
	if total > 8<<20 || len(files) > 200 {
		return fmt.Errorf("生成文件超过传输限额")
	}
	return nil
}

// Hosting credentials are available only to Git preparation, never to analysis agents.
func hostingCredential(key string) bool {
	key = strings.ToUpper(key)
	return strings.HasPrefix(key, "GITLAB_") || strings.HasPrefix(key, "GITHUB_") || key == "GH_TOKEN" || key == "GH_ENTERPRISE_TOKEN" || key == "GIT_ASKPASS"
}
