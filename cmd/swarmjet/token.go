package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

// runToken implements the "swarmjet token" mode. Only create is supported: agent
// tokens are minted by the platform for a Cell, never from the command line.
func runToken(ctx context.Context, s *store.Store, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: swarmjet token create --name <name> --scope default|admin")
	}
	switch args[0] {
	case "create":
		return runTokenCreate(ctx, s, args[1:], os.Stdout, os.Stderr)
	case "help", "--help", "-h":
		fmt.Fprintln(os.Stdout, "usage: swarmjet token create --name <name> --scope default|admin")
		return nil
	default:
		return fmt.Errorf("unknown token command %q; want create", args[0])
	}
}

// runTokenCreate mints a token, prints its plaintext exactly once to stdout, and
// keeps the status line on stderr so the command stays script-friendly.
func runTokenCreate(ctx context.Context, s *store.Store, args []string, stdout, stderr io.Writer) error {
	name, scope, err := parseTokenCreate(args)
	if err != nil {
		return err
	}
	plaintext, token, err := s.CreateToken(ctx, name, scope)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "token %s created (scope %s); copy it now, it is shown only once\n", token.ID, token.Scope)
	fmt.Fprintln(stdout, plaintext)
	return nil
}

// parseTokenCreate validates the command line without touching the database.
func parseTokenCreate(args []string) (string, domain.TokenScope, error) {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	name := fs.String("name", "", "human-readable token name")
	scope := fs.String("scope", string(domain.TokenDefault), "default or admin")
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	if fs.NArg() > 0 {
		return "", "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	trimmed := strings.TrimSpace(*name)
	if trimmed == "" {
		return "", "", fmt.Errorf("--name is required")
	}
	if len(trimmed) > 200 {
		return "", "", fmt.Errorf("--name must be at most 200 characters")
	}
	parsed := domain.TokenScope(strings.ToLower(strings.TrimSpace(*scope)))
	switch parsed {
	case domain.TokenDefault, domain.TokenAdmin:
		return trimmed, parsed, nil
	default:
		return "", "", fmt.Errorf("invalid --scope %q: want default or admin", *scope)
	}
}
