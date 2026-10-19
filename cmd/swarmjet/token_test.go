package main

import (
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestParseTokenCreate(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantName  string
		wantScope domain.TokenScope
		wantErr   string
	}{
		{name: "default", args: []string{"--name", "claude-code"}, wantName: "claude-code", wantScope: domain.TokenDefault},
		{name: "admin", args: []string{"--name", "ops", "--scope", "admin"}, wantName: "ops", wantScope: domain.TokenAdmin},
		{name: "trims", args: []string{"--name", "  laptop  ", "--scope", "DEFAULT"}, wantName: "laptop", wantScope: domain.TokenDefault},
		{name: "missing name", args: []string{"--scope", "admin"}, wantErr: "--name is required"},
		{name: "blank name", args: []string{"--name", "   "}, wantErr: "--name is required"},
		{name: "agent scope is not mintable from the CLI", args: []string{"--name", "x", "--scope", "agent"}, wantErr: "invalid --scope"},
		{name: "unknown scope", args: []string{"--name", "x", "--scope", "root"}, wantErr: "invalid --scope"},
		{name: "extra argument", args: []string{"--name", "x", "extra"}, wantErr: "unexpected argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, scope, err := parseTokenCreate(tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTokenCreate: %v", err)
			}
			if name != tc.wantName || scope != tc.wantScope {
				t.Fatalf("got %q/%q, want %q/%q", name, scope, tc.wantName, tc.wantScope)
			}
		})
	}
}
