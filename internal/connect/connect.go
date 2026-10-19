// Package connect renders the local Agent connection snippets shown by the
// console and by `swarmjet connect`. It deliberately has no database or
// network dependency: the caller mints the token and passes the plaintext in.
package connect

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ServerName is the MCP server name used by all generated client snippets.
const ServerName = "swarmjet"

// Client identifies a supported local Agent configuration format.
type Client string

const (
	ClientClaude Client = "claude"
	ClientCodex  Client = "codex"
	ClientJSON   Client = "json"
)

// ParseClient validates a --client value.
func ParseClient(value string) (Client, error) {
	switch client := Client(strings.ToLower(strings.TrimSpace(value))); client {
	case ClientClaude, ClientCodex, ClientJSON:
		return client, nil
	default:
		return "", fmt.Errorf("invalid client %q: want claude, codex or json", value)
	}
}

// MCPServer is the .mcp.json server entry.
type MCPServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// MCPConfig is the .mcp.json document written by clients that read that file.
type MCPConfig struct {
	MCPServers map[string]MCPServer `json:"mcpServers"`
}

// Bundle is the one-time connection payload returned by the token API.
type Bundle struct {
	MCPURL     string    `json:"mcpUrl"`
	ClaudeCode string    `json:"claudeCode"`
	Codex      string    `json:"codex"`
	JSON       MCPConfig `json:"json"`
}

// NewBundle renders all three client formats for one token.
func NewBundle(mcpURL, token string) Bundle {
	return Bundle{
		MCPURL:     mcpURL,
		ClaudeCode: claudeCommand(mcpURL, token),
		Codex:      codexConfig(mcpURL, token),
		JSON:       jsonConfig(mcpURL, token),
	}
}

// Render returns the config for one client. The token plaintext appears in the
// returned string exactly once and must never be logged.
func Render(client Client, mcpURL, token string) (string, error) {
	switch client {
	case ClientClaude:
		return claudeCommand(mcpURL, token), nil
	case ClientCodex:
		return codexConfig(mcpURL, token), nil
	case ClientJSON:
		return marshalJSONConfig(jsonConfig(mcpURL, token))
	default:
		return "", fmt.Errorf("invalid client %q: want claude, codex or json", client)
	}
}

func claudeCommand(mcpURL, token string) string {
	return fmt.Sprintf("claude mcp add --transport http %s %s --header %s",
		ServerName, mcpURL, strconv.Quote("Authorization: Bearer "+token))
}

// codexConfig follows the streamable HTTP MCP shape in the Codex
// configuration reference (developers.openai.com/codex/mcp): a
// [mcp_servers.<name>] table with `url` and `http_headers` for custom request
// headers. Codex has no `bearer_token` key, so the one-time token is sent as
// an explicit Authorization header.
func codexConfig(mcpURL, token string) string {
	return strings.Join([]string{
		"[mcp_servers." + ServerName + "]",
		"url = " + strconv.Quote(mcpURL),
		"http_headers = { Authorization = " + strconv.Quote("Bearer "+token) + " }",
		"",
	}, "\n")
}

func jsonConfig(mcpURL, token string) MCPConfig {
	return MCPConfig{MCPServers: map[string]MCPServer{
		ServerName: {
			Type:    "http",
			URL:     mcpURL,
			Headers: map[string]string{"Authorization": "Bearer " + token},
		},
	}}
}

func marshalJSONConfig(config MCPConfig) (string, error) {
	b, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}
