package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/connect"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type fakeTokenCreator struct {
	name      string
	scope     domain.TokenScope
	plaintext string
	token     domain.Token
}

func (f *fakeTokenCreator) CreateToken(_ context.Context, name string, scope domain.TokenScope) (string, domain.Token, error) {
	f.name, f.scope = name, scope
	return f.plaintext, f.token, nil
}

func TestRunConnectPrintsConfigAndKeepsPlaintextOffStderr(t *testing.T) {
	const plaintext = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		client connect.Client
		args   []string
		want   string
	}{
		{connect.ClientClaude, []string{"--client", "claude", "--url", "https://swarmjet.example/mcp"}, "claude mcp add --transport http swarmjet https://swarmjet.example/mcp"},
		{connect.ClientCodex, []string{"--client", "codex", "--url", "https://swarmjet.example/mcp"}, "[mcp_servers.swarmjet]"},
		{connect.ClientJSON, []string{"--client", "json", "--url", "https://swarmjet.example/mcp"}, `"mcpServers"`},
	} {
		t.Run(string(tc.client), func(t *testing.T) {
			creator := &fakeTokenCreator{plaintext: plaintext, token: domain.Token{ID: "token-id", Scope: domain.TokenDefault}}
			var stdout, stderr bytes.Buffer
			if err := runConnect(context.Background(), creator, tc.args, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			if creator.scope != domain.TokenDefault || creator.name != defaultConnectName(tc.client) {
				t.Fatalf("created %q/%q", creator.name, creator.scope)
			}
			if !strings.Contains(stdout.String(), tc.want) || !strings.Contains(stdout.String(), plaintext) {
				t.Fatalf("stdout missing config: %s", stdout.String())
			}
			if strings.Contains(stderr.String(), plaintext) {
				t.Fatalf("plaintext leaked to stderr: %s", stderr.String())
			}
			if !strings.Contains(stderr.String(), "token-id") {
				t.Fatalf("stderr missing token id: %s", stderr.String())
			}
		})
	}
}

func TestParseConnectDefaultsAndValidation(t *testing.T) {
	t.Setenv("HTTP_PORT", "9123")
	opts, err := parseConnect([]string{"--client", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.URL != "http://127.0.0.1:9123/mcp" || opts.Name != "codex" {
		t.Fatalf("unexpected defaults %+v", opts)
	}
	opts, err = parseConnect([]string{"--client", "claude", "--name", " laptop "})
	if err != nil || opts.Name != "laptop" {
		t.Fatalf("name parsing %+v %v", opts, err)
	}
	for _, args := range [][]string{
		{},
		{"--client", "vscode"},
		{"--client", "claude", "--url", "ftp://example/mcp"},
		{"--client", "claude", "--url", "not a url"},
		{"--client", "claude", "extra"},
		{"--client", "claude", "--name", strings.Repeat("x", 201)},
	} {
		if _, err := parseConnect(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
