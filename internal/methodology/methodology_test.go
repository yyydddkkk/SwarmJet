package methodology

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func validManifest() domain.MethodologyManifest {
	return domain.MethodologyManifest{
		DefaultRole:    "worker",
		StandingOrders: []string{"Ask for a non-author verification before merging.", "Keep the branch focused."},
		Roles: map[string]domain.Role{
			"worker": {
				Description: "Write code and open a pull request.",
				Prompt:      "roles/worker.md",
				Lifecycle:   "task",
				Permissions: domain.RolePermissions{Publish: true, Message: true, Spawn: []string{"verifier"}},
			},
			"verifier": {
				Description: "Review independently.",
				Prompt:      "roles/verifier.md",
				Lifecycle:   "task",
				Permissions: domain.RolePermissions{Message: true, Attest: true},
				DenyTools:   []string{"edit", "write"},
			},
		},
		Events: []domain.EventRule{{Event: "issue_labeled", Label: "swarmjet", Role: "worker"}},
		Merge: domain.MergePolicy{
			RequireAttestation: true,
			AcceptResults:      []domain.AttestResult{domain.AttestPass, domain.AttestPassNotes},
			RequireNonAuthor:   true,
			RequireChecks:      true,
		},
		Limits: domain.Limits{MaxConcurrentAgents: 2, MaxHops: 4, MaxCostPerAgentUSD: 5},
	}
}

func validFiles() []domain.MethodologyFile {
	return []domain.MethodologyFile{
		{Path: "roles/worker.md", Content: "write"},
		{Path: "roles/verifier.md", Content: "review"},
		{Path: "skills/testing/SKILL.md", Content: "test"},
	}
}

func TestParseStrictUsesContractNames(t *testing.T) {
	raw := []byte(`
defaultRole: worker
standingOrders: ["verify first"]
roles:
  worker:
    description: write
    prompt: roles/worker.md
    lifecycle: task
    permissions: { publish: true, message: true, spawn: [verifier] }
  verifier:
    prompt: roles/verifier.md
    permissions: { message: true, attest: true }
events:
  - { event: issue_labeled, label: swarmjet, role: worker }
merge:
  requireAttestation: true
  requireNonAuthor: true
  requireChecks: true
  auto: false
limits:
  maxCostPerAgentUsd: 5
`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.DefaultRole != "worker" || len(m.Roles) != 2 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if m.Roles["worker"].Permissions.Spawn[0] != "verifier" {
		t.Fatalf("spawn not decoded: %+v", m.Roles["worker"])
	}
	if m.Merge.RequireNonAuthor != true || m.Limits.MaxCostPerAgentUSD != 5 {
		t.Fatalf("merge or limits not decoded: %+v", m)
	}
	if err := Validate(m, []domain.MethodologyFile{
		{Path: "roles/worker.md"}, {Path: "roles/verifier.md"},
	}); err != nil {
		t.Fatalf("valid parsed manifest rejected: %v", err)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	cases := map[string]string{
		"top level":  "defaultRole: worker\nroles: {worker: {permissions: {publish: true}}}\nmystery: true\n",
		"role":       "defaultRole: worker\nroles: {worker: {permissions: {publish: true}, teleport: true}}\n",
		"permission": "defaultRole: worker\nroles: {worker: {permissions: {publish: true, admin: true}}}\n",
		"merge":      "defaultRole: worker\nroles: {worker: {permissions: {publish: true}}}\nmerge: {mergeNow: true}\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("expected unknown-field error, got %v", err)
			}
		})
	}
}

func TestParseRejectsMalformedAndMultipleDocuments(t *testing.T) {
	if _, err := Parse([]byte("defaultRole: [")); err == nil {
		t.Fatal("malformed YAML accepted")
	}
	if _, err := Parse([]byte("defaultRole: worker\n---\ndefaultRole: other\n")); err == nil {
		t.Fatal("multiple documents accepted")
	}
	if _, err := Parse(nil); err == nil {
		t.Fatal("empty document accepted")
	}
	if _, err := Parse([]byte("- a\n- b\n")); err == nil {
		t.Fatal("sequence document accepted")
	}
}

func TestValidateRules(t *testing.T) {
	baseManifest := validManifest()
	baseFiles := validFiles()

	cases := []struct {
		name    string
		mutate  func(*domain.MethodologyManifest, *[]domain.MethodologyFile)
		wantErr string
	}{
		{name: "valid"},
		{name: "no roles", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) { m.Roles = nil }, wantErr: "at least one role"},
		{name: "no default role", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) { m.DefaultRole = "" }, wantErr: "defaultRole is required"},
		{name: "unknown default role", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) { m.DefaultRole = "ghost" }, wantErr: "not defined"},
		{name: "unknown lifecycle", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) {
			role := m.Roles["worker"]
			role.Lifecycle = "immortal"
			m.Roles["worker"] = role
		}, wantErr: "unknown lifecycle"},
		{name: "missing prompt file", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) {
			role := m.Roles["worker"]
			role.Prompt = "roles/missing.md"
			m.Roles["worker"] = role
		}, wantErr: "is not one of the files"},
		{name: "unsafe prompt path", mutate: func(m *domain.MethodologyManifest, f *[]domain.MethodologyFile) {
			role := m.Roles["worker"]
			role.Prompt = "../escape.md"
			m.Roles["worker"] = role
		}, wantErr: "path"},
		{name: "unknown spawn role", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) {
			role := m.Roles["worker"]
			role.Permissions.Spawn = []string{"ghost"}
			m.Roles["worker"] = role
		}, wantErr: "unknown role"},
		{name: "empty spawn role", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) {
			role := m.Roles["worker"]
			role.Permissions.Spawn = []string{" "}
			m.Roles["worker"] = role
		}, wantErr: "empty spawn role"},
		{name: "unknown event role", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) {
			m.Events[0].Role = "ghost"
		}, wantErr: "unknown role"},
		{name: "empty event name", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) {
			m.Events[0].Event = ""
		}, wantErr: "no event name"},
		{name: "invalid attest result", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) {
			m.Merge.AcceptResults = []domain.AttestResult{"maybe"}
		}, wantErr: "unknown accepted attest result"},
		{name: "invalid merge method", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) {
			m.Merge.Method = "fast-forward"
		}, wantErr: "unknown merge method"},
		{name: "negative count limit", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) {
			m.Limits.MaxHops = -1
		}, wantErr: "must not be negative"},
		{name: "negative cost limit", mutate: func(m *domain.MethodologyManifest, _ *[]domain.MethodologyFile) {
			m.Limits.MaxCostPerDayUSD = -0.01
		}, wantErr: "must not be negative"},
		{name: "absolute file path", mutate: func(_ *domain.MethodologyManifest, f *[]domain.MethodologyFile) {
			(*f)[0].Path = "/etc/passwd"
		}, wantErr: "must be relative"},
		{name: "dotdot file path", mutate: func(_ *domain.MethodologyManifest, f *[]domain.MethodologyFile) {
			(*f)[0].Path = "../secret"
		}, wantErr: ".."},
		{name: "unclean dotdot path", mutate: func(_ *domain.MethodologyManifest, f *[]domain.MethodologyFile) {
			(*f)[0].Path = "a/../../b"
		}, wantErr: "not clean"},
		{name: "backslash path", mutate: func(_ *domain.MethodologyManifest, f *[]domain.MethodologyFile) {
			(*f)[0].Path = "roles\\worker.md"
		}, wantErr: "forward slashes"},
		{name: "reserved manifest path", mutate: func(_ *domain.MethodologyManifest, f *[]domain.MethodologyFile) {
			(*f)[0].Path = ManifestName
		}, wantErr: "reserved"},
		{name: "duplicate file", mutate: func(_ *domain.MethodologyManifest, f *[]domain.MethodologyFile) {
			*f = append(*f, (*f)[0])
		}, wantErr: "more than once"},
		{name: "too many files", mutate: func(_ *domain.MethodologyManifest, f *[]domain.MethodologyFile) {
			*f = make([]domain.MethodologyFile, MaxFiles+1)
			for i := range *f {
				(*f)[i] = domain.MethodologyFile{Path: "skills/" + strings.Repeat("x", i+1)}
			}
		}, wantErr: "exceed the limit"},
		{name: "too large", mutate: func(_ *domain.MethodologyManifest, f *[]domain.MethodologyFile) {
			*f = []domain.MethodologyFile{{Path: "big.txt", Content: strings.Repeat("x", MaxTotalBytes+1)}}
		}, wantErr: "exceed the limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := baseManifest
			m.Roles = map[string]domain.Role{}
			for k, v := range baseManifest.Roles {
				m.Roles[k] = v
			}
			m.Events = append([]domain.EventRule(nil), baseManifest.Events...)
			m.Merge.AcceptResults = append([]domain.AttestResult(nil), baseManifest.Merge.AcceptResults...)
			m.StandingOrders = append([]string(nil), baseManifest.StandingOrders...)
			files := append([]domain.MethodologyFile(nil), baseFiles...)
			if tc.mutate != nil {
				tc.mutate(&m, &files)
			}
			err := Validate(m, files)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("valid case rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidatePath(t *testing.T) {
	for _, ok := range []string{"roles/worker.md", "skills/a/SKILL.md", "a.txt", "a-b_c/d.e"} {
		if err := ValidatePath(ok); err != nil {
			t.Errorf("ValidatePath(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/abs", "../x", "a/../b", "a/./b", "a//b", "a\\b", "C:/x", "~x", ManifestName} {
		if err := ValidatePath(bad); err == nil {
			t.Errorf("ValidatePath(%q) accepted", bad)
		}
	}
}

func TestDigestIsStableAndSensitive(t *testing.T) {
	m := validManifest()
	files := validFiles()
	first, err := Digest(m, files)
	if err != nil {
		t.Fatal(err)
	}

	reordered := []domain.MethodologyFile{files[2], files[0], files[1]}
	second, err := Digest(m, reordered)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("digest changed with file order: %s != %s", first, second)
	}

	// Map iteration order must not matter either.
	other := validManifest()
	otherMap := map[string]domain.Role{}
	otherMap["verifier"] = other.Roles["verifier"]
	otherMap["worker"] = other.Roles["worker"]
	other.Roles = otherMap
	third, err := Digest(other, files)
	if err != nil {
		t.Fatal(err)
	}
	if first != third {
		t.Fatalf("digest changed with role map order: %s != %s", first, third)
	}

	changed := append([]domain.MethodologyFile(nil), files...)
	changed[0].Content = "different"
	fourth, err := Digest(m, changed)
	if err != nil {
		t.Fatal(err)
	}
	if first == fourth {
		t.Fatal("digest did not change when content changed")
	}

	renamed := append([]domain.MethodologyFile(nil), files...)
	renamed[0].Path = "roles/other.md"
	fifth, err := Digest(m, renamed)
	if err != nil {
		t.Fatal(err)
	}
	if first == fifth {
		t.Fatal("digest did not change when a path changed")
	}
	if len(first) != 64 {
		t.Fatalf("digest is not sha256 hex: %q", first)
	}
}

func TestResolve(t *testing.T) {
	m := validManifest()
	role, err := Resolve(m, "")
	if err != nil || role.Permissions.Publish != true {
		t.Fatalf("default role = %+v, %v", role, err)
	}
	role, err = Resolve(m, "verifier")
	if err != nil || !role.Permissions.Attest || role.Permissions.Publish {
		t.Fatalf("verifier = %+v, %v", role, err)
	}
	if _, err = Resolve(m, "ghost"); err == nil {
		t.Fatal("unknown role resolved")
	}
}

func TestBuiltinIsMinimalValidAndSafeToMerge(t *testing.T) {
	m, files := Builtin()
	if m.DefaultRole != "worker" || len(m.Roles) != 1 || len(files) != 0 {
		t.Fatalf("unexpected builtin: %+v files=%v", m, files)
	}
	role := m.Roles["worker"]
	if !role.Permissions.Publish || !role.Permissions.Message || role.Permissions.Attest {
		t.Fatalf("builtin permissions = %+v", role.Permissions)
	}
	if m.Merge.RequireAttestation || !m.Merge.RequireChecks || m.Merge.Auto {
		t.Fatalf("builtin merge policy = %+v", m.Merge)
	}
	if err := Validate(m, files); err != nil {
		t.Fatalf("builtin does not validate: %v", err)
	}
	if _, err := Digest(m, files); err != nil {
		t.Fatal(err)
	}
}

type fakeSource struct {
	m     domain.Methodology
	files []domain.MethodologyFile
	err   error
}

func (f *fakeSource) ActiveMethodology(context.Context, string) (domain.Methodology, []domain.MethodologyFile, error) {
	return f.m, f.files, f.err
}

func TestForAgentReturnsConfigFilesAndStandingRules(t *testing.T) {
	m := validManifest()
	files := validFiles()
	source := &fakeSource{m: domain.Methodology{HiveID: "h1", Version: 3, Manifest: m}, files: files}
	config, err := ForAgent(context.Background(), source, domain.Agent{HiveID: "h1", RoleName: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	if config.RoleName != "verifier" || config.Builtin {
		t.Fatalf("unexpected config: %+v", config)
	}
	if !config.ReadOnly() {
		t.Fatal("verifier should be read-only")
	}
	if got := config.ConfigFiles["methodology/roles/verifier.md"]; got != "review" {
		t.Fatalf("role prompt not in ConfigFiles: %q", got)
	}
	if got := config.ConfigFiles["methodology/skills/testing/SKILL.md"]; got != "test" {
		t.Fatalf("skill not in ConfigFiles: %q", got)
	}
	if len(config.StandingOrders) != 2 || config.StandingOrders[0] != m.StandingOrders[0] {
		t.Fatalf("standing orders = %v", config.StandingOrders)
	}
	header := config.PromptHeader()
	for _, want := range []string{"## Hive methodology", `"verifier"`, "Review independently.", m.StandingOrders[0], "attest=true", "publish=false"} {
		if !strings.Contains(header, want) {
			t.Fatalf("prompt header missing %q:\n%s", want, header)
		}
	}
}

func TestForAgentFallsBackToBuiltin(t *testing.T) {
	config, err := ForAgent(context.Background(), &fakeSource{err: domain.ErrNotFound}, domain.Agent{HiveID: "h1"})
	if err != nil {
		t.Fatal(err)
	}
	if !config.Builtin || config.RoleName != "worker" || !config.Role.Permissions.Publish {
		t.Fatalf("builtin fallback = %+v", config)
	}
	if config.ReadOnly() {
		t.Fatal("builtin worker must be able to publish")
	}
	if len(config.ConfigFiles) != 0 {
		t.Fatalf("builtin should not deliver files: %v", config.ConfigFiles)
	}

	config, err = ForAgent(context.Background(), nil, domain.Agent{HiveID: "h1"})
	if err != nil || !config.Builtin {
		t.Fatalf("nil source = %+v, %v", config, err)
	}
}

func TestForAgentPropagatesReadErrorsAndTracksVersionChanges(t *testing.T) {
	boom := errors.New("database is down")
	if _, err := ForAgent(context.Background(), &fakeSource{err: boom}, domain.Agent{HiveID: "h1"}); !errors.Is(err, boom) {
		t.Fatalf("read error = %v", err)
	}

	m := validManifest()
	source := &fakeSource{m: domain.Methodology{HiveID: "h1", Manifest: m}, files: validFiles()}
	first, err := ForAgent(context.Background(), source, domain.Agent{HiveID: "h1", RoleName: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	m.StandingOrders = []string{"the new rule"}
	source.m.Manifest = m
	second, err := ForAgent(context.Background(), source, domain.Agent{HiveID: "h1", RoleName: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.PromptHeader(), "the new rule") {
		t.Fatal("first prompt unexpectedly has the new rule")
	}
	if !strings.Contains(second.PromptHeader(), "the new rule") {
		t.Fatal("next prompt did not pick up the new active version immediately")
	}
}
