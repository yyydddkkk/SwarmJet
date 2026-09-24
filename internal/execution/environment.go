package execution

import (
	"context"
	"strings"
	"swarmjet/internal/domain"
)

// EnvironmentBackend supplies node-local CLI credentials. Explicit phase and
// versioned network values from the request take precedence over defaults.
type EnvironmentBackend struct {
	AgentBackend
	Env map[string]string
}

func (b *EnvironmentBackend) Start(ctx context.Context, r domain.AgentRequest) (domain.Execution, error) {
	env := map[string]string{}
	for k, v := range b.Env {
		if strings.HasPrefix(k, "GITLAB_") || githubCredential(k) || k == "GIT_ASKPASS" {
			continue
		}
		env[k] = v
	}
	for k, v := range r.Env {
		if githubCredential(k) {
			continue
		}
		env[k] = v
	}
	r.Env = env
	return b.AgentBackend.Start(ctx, r)
}

func githubCredential(key string) bool {
	return strings.HasPrefix(key, "GITHUB_") || key == "GH_TOKEN" || key == "GH_ENTERPRISE_TOKEN"
}
