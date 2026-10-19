package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/connect"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// tokenCreator is the minimal store surface the connect command needs. It
// keeps the command testable without a database.
type tokenCreator interface {
	CreateToken(context.Context, string, domain.TokenScope) (string, domain.Token, error)
}

type connectOptions struct {
	Client connect.Client
	Name   string
	URL    string
}

// runConnect implements `swarmjet connect`: it mints one default-scope token
// and prints the matching local Agent configuration. The plaintext is written
// once to stdout and never to stderr or logs.
func runConnect(ctx context.Context, tokens tokenCreator, args []string, stdout, stderr io.Writer) error {
	opts, err := parseConnect(args)
	if err != nil {
		return err
	}
	plaintext, token, err := tokens.CreateToken(ctx, opts.Name, domain.TokenDefault)
	if err != nil {
		return err
	}
	config, err := connect.Render(opts.Client, opts.URL, plaintext)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "token %s created (scope default); the token in this config is shown only once\n", token.ID)
	if !strings.HasSuffix(config, "\n") {
		config += "\n"
	}
	_, err = io.WriteString(stdout, config)
	return err
}

func parseConnect(args []string) (connectOptions, error) {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	clientValue := fs.String("client", "", "claude, codex or json")
	name := fs.String("name", "", "token name (defaults to the client name)")
	mcpURL := fs.String("url", defaultConnectURL(), "MCP endpoint")
	if err := fs.Parse(args); err != nil {
		return connectOptions{}, err
	}
	if fs.NArg() > 0 {
		return connectOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	client, err := connect.ParseClient(*clientValue)
	if err != nil {
		return connectOptions{}, err
	}
	trimmedName := strings.TrimSpace(*name)
	if trimmedName == "" {
		trimmedName = defaultConnectName(client)
	}
	if len(trimmedName) > 200 {
		return connectOptions{}, fmt.Errorf("--name must be at most 200 characters")
	}
	endpoint := strings.TrimSpace(*mcpURL)
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return connectOptions{}, fmt.Errorf("--url must be an http(s) MCP endpoint")
	}
	return connectOptions{Client: client, Name: trimmedName, URL: endpoint}, nil
}

func defaultConnectURL() string {
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "8180"
	}
	return "http://127.0.0.1:" + port + "/mcp"
}

func defaultConnectName(client connect.Client) string {
	switch client {
	case connect.ClientClaude:
		return "claude-code"
	case connect.ClientCodex:
		return "codex"
	default:
		return connect.ServerName
	}
}
