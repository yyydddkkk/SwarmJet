package mcp

import (
	"context"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type fakeCredentialWriter struct {
	provider string
	key      string
	masked   string
}

func (f *fakeCredentialWriter) SetModelCredential(_ context.Context, provider, key string) (string, error) {
	f.provider, f.key = provider, key
	if f.masked == "" {
		f.masked = "••••test"
	}
	return f.masked, nil
}

func TestSetModelCredentialRequiresAdminAndMasks(t *testing.T) {
	writer := &fakeCredentialWriter{masked: "••••abcd"}
	ts := &toolset{
		opts:  Options{Credentials: writer},
		token: domain.Token{Scope: domain.TokenAdmin},
	}
	if err := ts.authorize("set_model_credential"); err != nil {
		t.Fatalf("admin denied: %v", err)
	}
	_, output, err := ts.setModelCredential(context.Background(), nil, setModelCredentialInput{
		Provider: "DeepSeek", APIKey: "sk-test-value-abcd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.Provider != "deepseek" || output.Masked != "••••abcd" || !output.Configured {
		t.Fatalf("output = %+v", output)
	}
	if writer.provider != "deepseek" || writer.key != "sk-test-value-abcd" {
		t.Fatalf("writer = %+v", writer)
	}

	defaultToken := &toolset{token: domain.Token{Scope: domain.TokenDefault}}
	if err := defaultToken.authorize("set_model_credential"); err == nil {
		t.Fatal("default token was allowed to write a credential")
	}
	agentToken := &toolset{
		token: domain.Token{Scope: domain.TokenAgent, AgentID: "agent-1"},
	}
	agentToken.actor.agent = &domain.Agent{ID: "agent-1", Role: domain.RoleForager}
	if err := agentToken.authorize("set_model_credential"); err == nil {
		t.Fatal("agent token was allowed to write a credential")
	}
}

func TestSetModelCredentialValidatesInput(t *testing.T) {
	ts := &toolset{
		opts:  Options{Credentials: &fakeCredentialWriter{}},
		token: domain.Token{Scope: domain.TokenAdmin},
	}
	if _, _, err := ts.setModelCredential(context.Background(), nil, setModelCredentialInput{
		Provider: "../bad", APIKey: "sk-test",
	}); err == nil {
		t.Fatal("invalid provider accepted")
	}
	if _, _, err := ts.setModelCredential(context.Background(), nil, setModelCredentialInput{
		Provider: "deepseek", APIKey: "",
	}); err == nil {
		t.Fatal("empty key accepted")
	}
}
