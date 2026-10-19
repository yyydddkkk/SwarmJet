package connect

import (
	"encoding/json"
	"strings"
	"testing"
)

const testToken = "0123456789abcdef0123456789abcdef"

func TestRenderClaudeCommand(t *testing.T) {
	got, err := Render(ClientClaude, "http://127.0.0.1:8180/mcp", testToken)
	if err != nil {
		t.Fatal(err)
	}
	want := `claude mcp add --transport http swarmjet http://127.0.0.1:8180/mcp --header "Authorization: Bearer ` + testToken + `"`
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestRenderCodexHTTPConfig(t *testing.T) {
	got, err := Render(ClientCodex, "https://swarmjet.example/mcp", testToken)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[mcp_servers.swarmjet]",
		`url = "https://swarmjet.example/mcp"`,
		`http_headers = { Authorization = "Bearer ` + testToken + `" }`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
}

func TestRenderJSONConfig(t *testing.T) {
	got, err := Render(ClientJSON, "https://swarmjet.example/mcp", testToken)
	if err != nil {
		t.Fatal(err)
	}
	var config MCPConfig
	if err = json.Unmarshal([]byte(got), &config); err != nil {
		t.Fatal(err)
	}
	server := config.MCPServers[ServerName]
	if server.Type != "http" || server.URL != "https://swarmjet.example/mcp" {
		t.Fatalf("unexpected server %+v", server)
	}
	if server.Headers["Authorization"] != "Bearer "+testToken {
		t.Fatalf("unexpected headers %+v", server.Headers)
	}
}

func TestBundleContainsAllFormats(t *testing.T) {
	bundle := NewBundle("http://127.0.0.1:8180/mcp", testToken)
	if bundle.MCPURL != "http://127.0.0.1:8180/mcp" || !strings.Contains(bundle.ClaudeCode, testToken) ||
		!strings.Contains(bundle.Codex, testToken) || bundle.JSON.MCPServers[ServerName].URL == "" {
		t.Fatalf("incomplete bundle %+v", bundle)
	}
}

func TestRenderRejectsUnknownClient(t *testing.T) {
	if _, err := Render(Client("vim"), "http://localhost/mcp", testToken); err == nil {
		t.Fatal("unknown client accepted")
	}
}

func TestParseClient(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Client
		ok   bool
	}{{"claude", ClientClaude, true}, {" CODEX ", ClientCodex, true}, {"json", ClientJSON, true}, {"vscode", "", false}, {"", "", false}} {
		got, err := ParseClient(tc.in)
		if tc.ok != (err == nil) || got != tc.want {
			t.Fatalf("ParseClient(%q) = %q, %v", tc.in, got, err)
		}
	}
}
