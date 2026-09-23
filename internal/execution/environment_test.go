package execution

import (
	"context"
	"swarmjet/internal/domain"
	"testing"
)

type envBackend struct {
	AgentBackend
	request domain.AgentRequest
}

func (b *envBackend) Start(_ context.Context, r domain.AgentRequest) (domain.Execution, error) {
	b.request = r
	return domain.Execution{}, nil
}
func TestNodeDefaultsCannotIntroduceGitLabPhaseCredential(t *testing.T) {
	backend := &envBackend{}
	wrapper := EnvironmentBackend{AgentBackend: backend, Env: map[string]string{"GITLAB_TOKEN": "must-not-leak", "GITLAB_MERGE_TOKEN": "must-not-leak", "OPENAI_API_KEY": "node-model-key", "HTTP_PROXY": "http://old-proxy"}}
	_, e := wrapper.Start(context.Background(), domain.AgentRequest{Role: "review", Env: map[string]string{"HTTP_PROXY": ""}})
	if e != nil {
		t.Fatal(e)
	}
	if backend.request.Env["GITLAB_TOKEN"] != "" || backend.request.Env["GITLAB_MERGE_TOKEN"] != "" || backend.request.Env["HTTP_PROXY"] != "" || backend.request.Env["OPENAI_API_KEY"] != "node-model-key" {
		t.Fatal("unexpected default credential or network merge")
	}
	_, e = wrapper.Start(context.Background(), domain.AgentRequest{Role: "merge", Env: map[string]string{"GITLAB_TOKEN": "phase-token"}})
	if e != nil || backend.request.Env["GITLAB_TOKEN"] != "phase-token" {
		t.Fatal("phase credential was lost")
	}
}
