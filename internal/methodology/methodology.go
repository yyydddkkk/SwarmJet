// Package methodology owns the format of a Hive's methodology package: it
// parses methodology.yaml, validates the referenced roles and files, computes a
// stable digest, and resolves the effective role for an Agent.
//
// A methodology version is a manifest plus a set of files. The platform stores
// the versions, delivers the files to Cells and enforces the permissions in the
// manifest; it never invents a workflow of its own.
package methodology

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

const (
	// ManifestName is the required file at the root of every methodology.
	ManifestName = "methodology.yaml"
	// ConfigPrefix is the directory under the CLI configuration directory in
	// which a Cell receives the methodology files.
	ConfigPrefix = "methodology/"

	// MaxFiles caps the number of files in one version.
	MaxFiles = 500
	// MaxTotalBytes caps the sum of all file contents in one version.
	MaxTotalBytes = 2 << 20 // 2 MiB
)

// Change actions recorded in the append-only methodology history.
const (
	ActionPublish  = "publish"
	ActionActivate = "activate"
)

// Parse decodes methodology.yaml. Unknown fields are rejected so a typo cannot
// silently change how a swarm behaves; all keys use the JSON names from
// domain.MethodologyManifest (for example defaultRole and standingOrders).
func Parse(data []byte) (domain.MethodologyManifest, error) {
	var zero domain.MethodologyManifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return zero, fmt.Errorf("methodology: parse %s: %w", ManifestName, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return zero, fmt.Errorf("methodology: %s must contain exactly one YAML document", ManifestName)
		}
		return zero, fmt.Errorf("methodology: parse %s: %w", ManifestName, err)
	}
	if raw == nil {
		return zero, fmt.Errorf("methodology: %s must be a mapping", ManifestName)
	}

	// yaml.v3 has no notion of the JSON tags on the domain types. Converting
	// through JSON lets the contract types stay the single source of truth and
	// still gives strict unknown-field checking.
	body, err := json.Marshal(raw)
	if err != nil {
		return zero, fmt.Errorf("methodology: %s must use string mapping keys: %w", ManifestName, err)
	}
	jdec := json.NewDecoder(bytes.NewReader(body))
	jdec.DisallowUnknownFields()
	var m domain.MethodologyManifest
	if err := jdec.Decode(&m); err != nil {
		return zero, fmt.Errorf("methodology: parse %s: %w", ManifestName, err)
	}
	return m, nil
}

// Validate checks the contract rules for a manifest and its files. It does not
// parse YAML; call Parse first so unknown fields are also rejected.
func Validate(m domain.MethodologyManifest, files []domain.MethodologyFile) error {
	if len(m.Roles) == 0 {
		return errors.New("methodology: at least one role is required")
	}
	if strings.TrimSpace(m.DefaultRole) == "" {
		return errors.New("methodology: defaultRole is required")
	}
	if m.DefaultRole != strings.TrimSpace(m.DefaultRole) {
		return fmt.Errorf("methodology: defaultRole %q has surrounding whitespace", m.DefaultRole)
	}
	if _, ok := m.Roles[m.DefaultRole]; !ok {
		return fmt.Errorf("methodology: defaultRole %q is not defined in roles", m.DefaultRole)
	}

	if len(files) > MaxFiles {
		return fmt.Errorf("methodology: %d files exceed the limit of %d", len(files), MaxFiles)
	}
	paths := make(map[string]struct{}, len(files))
	total := 0
	for i, f := range files {
		if err := ValidatePath(f.Path); err != nil {
			return fmt.Errorf("methodology: file %d: %w", i, err)
		}
		if _, exists := paths[f.Path]; exists {
			return fmt.Errorf("methodology: file %q is listed more than once", f.Path)
		}
		paths[f.Path] = struct{}{}
		total += len(f.Content)
		if total > MaxTotalBytes {
			return fmt.Errorf("methodology: file contents exceed the limit of %d bytes", MaxTotalBytes)
		}
	}

	for _, name := range sortedRoleNames(m.Roles) {
		role := m.Roles[name]
		if strings.TrimSpace(name) == "" {
			return errors.New("methodology: role names must not be empty")
		}
		if name != strings.TrimSpace(name) {
			return fmt.Errorf("methodology: role name %q has surrounding whitespace", name)
		}
		switch role.Lifecycle {
		case "", "task", "resident":
		default:
			return fmt.Errorf("methodology: role %q has unknown lifecycle %q", name, role.Lifecycle)
		}
		if role.Prompt != "" {
			if err := ValidatePath(role.Prompt); err != nil {
				return fmt.Errorf("methodology: role %q prompt: %w", name, err)
			}
			if _, ok := paths[role.Prompt]; !ok {
				return fmt.Errorf("methodology: role %q prompt %q is not one of the files", name, role.Prompt)
			}
		}
		for _, target := range role.Permissions.Spawn {
			if strings.TrimSpace(target) == "" {
				return fmt.Errorf("methodology: role %q has an empty spawn role", name)
			}
			if _, ok := m.Roles[target]; !ok {
				return fmt.Errorf("methodology: role %q may spawn unknown role %q", name, target)
			}
		}
	}

	for i, rule := range m.Events {
		if strings.TrimSpace(rule.Event) == "" {
			return fmt.Errorf("methodology: event %d has no event name", i)
		}
		if _, ok := m.Roles[rule.Role]; !ok {
			return fmt.Errorf("methodology: event %d references unknown role %q", i, rule.Role)
		}
	}

	for _, result := range m.Merge.AcceptResults {
		switch result {
		case domain.AttestPass, domain.AttestPassNotes, domain.AttestFail, domain.AttestBlocked:
		default:
			return fmt.Errorf("methodology: unknown accepted attest result %q", result)
		}
	}
	switch m.Merge.Method {
	case "", "squash", "merge", "rebase":
	default:
		return fmt.Errorf("methodology: unknown merge method %q", m.Merge.Method)
	}

	if m.Limits.MaxConcurrentAgents < 0 || m.Limits.MaxHops < 0 || m.Limits.MaxAgentsPerChain < 0 {
		return errors.New("methodology: limits must not be negative")
	}
	if m.Limits.MaxCostPerAgentUSD < 0 || m.Limits.MaxCostPerDayUSD < 0 {
		return errors.New("methodology: cost limits must not be negative")
	}
	return nil
}

// ValidatePath accepts only a clean relative slash-separated path. It rejects
// absolute paths, "..", backslashes, empty segments and the reserved manifest
// name, so a file can never escape the Cell's methodology directory.
func ValidatePath(p string) error {
	if p == "" {
		return errors.New("path is empty")
	}
	if strings.ContainsRune(p, 0) {
		return errors.New("path contains a NUL byte")
	}
	if strings.Contains(p, "\\") {
		return errors.New("path must use forward slashes")
	}
	if path.IsAbs(p) || strings.HasPrefix(p, "/") {
		return fmt.Errorf("path %q must be relative", p)
	}
	if len(p) >= 2 && p[1] == ':' {
		return fmt.Errorf("path %q must not start with a drive letter", p)
	}
	if p == ManifestName {
		return fmt.Errorf("path %q is reserved for the manifest", p)
	}
	clean := path.Clean(p)
	if clean != p {
		return fmt.Errorf("path %q is not clean; use a relative path without '.', '..' or empty segments", p)
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path %q must not contain '..'", p)
	}
	if strings.HasPrefix(p, "~") {
		return fmt.Errorf("path %q must not start with '~'", p)
	}
	return nil
}

// Digest is the stable identity of a methodology version. It hashes the
// manifest and every file in path order, so reordering the request does not
// change the digest while any content or path change does.
func Digest(m domain.MethodologyManifest, files []domain.MethodologyFile) (string, error) {
	manifest, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("methodology: encode manifest: %w", err)
	}
	sorted := append([]domain.MethodologyFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Path != sorted[j].Path {
			return sorted[i].Path < sorted[j].Path
		}
		return sorted[i].Content < sorted[j].Content
	})

	h := sha256.New()
	writePart(h, []byte("swarmjet/methodology/v1"))
	writePart(h, manifest)
	writePart(h, []byte(fmt.Sprintf("%d", len(sorted))))
	for _, f := range sorted {
		writePart(h, []byte(f.Path))
		writePart(h, []byte(f.Content))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writePart(w io.Writer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = w.Write(length[:])
	_, _ = w.Write(value)
}

// Resolve returns the effective role. An empty roleName selects defaultRole.
func Resolve(m domain.MethodologyManifest, roleName string) (domain.Role, error) {
	name := strings.TrimSpace(roleName)
	if name == "" {
		name = m.DefaultRole
	}
	role, ok := m.Roles[name]
	if !ok {
		return domain.Role{}, fmt.Errorf("methodology: role %q is not defined", name)
	}
	return role, nil
}

// Builtin is the minimal methodology the platform falls back to before a Hive
// publishes one: one worker role that may publish and message, no attestation
// requirement, CI checks required before merging and no automatic merge.
func Builtin() (domain.MethodologyManifest, []domain.MethodologyFile) {
	return domain.MethodologyManifest{
		DefaultRole: "worker",
		Roles: map[string]domain.Role{
			"worker": {
				Description: "Write code and open a pull request.",
				Lifecycle:   "task",
				Permissions: domain.RolePermissions{Publish: true, Message: true},
			},
		},
		Merge: domain.MergePolicy{RequireChecks: true, Auto: false},
	}, nil
}

// ActiveSource reads a Hive's active methodology. *store.Store implements it.
type ActiveSource interface {
	ActiveMethodology(ctx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error)
}

// AgentConfig is one Agent's view of the active methodology.
//
// ConfigFiles are the methodology files under the CLI configuration directory
// (keys already include ConfigPrefix). They are written when the Cell starts,
// so a change takes effect only on the Agent's next Start. StandingOrders and
// the role description are injected into every turn, so a new active version
// applies to the next prompt immediately.
type AgentConfig struct {
	Methodology    domain.Methodology
	RoleName       string
	Role           domain.Role
	ConfigFiles    map[string]string
	StandingOrders []string
	Builtin        bool
}

// ReadOnly reports whether the role may not publish.
func (c AgentConfig) ReadOnly() bool {
	return !c.Role.Permissions.Publish
}

// Model is the role's model override; empty means the Hive default.
func (c AgentConfig) Model() string {
	return c.Role.Model
}

// PromptHeader renders the standing rules injected at the start of every turn:
// the Agent's role, the permissions the platform enforces for it, and the
// methodology's standing orders.
func (c AgentConfig) PromptHeader() string {
	var b strings.Builder
	b.WriteString("## Hive methodology\n")
	fmt.Fprintf(&b, "Your role is %q.", c.RoleName)
	if description := strings.TrimSpace(c.Role.Description); description != "" {
		fmt.Fprintf(&b, " %s", description)
	}
	b.WriteString("\n")
	permissions := c.Role.Permissions
	fmt.Fprintf(&b, "Permissions: publish=%t message=%t attest=%t merge=%t methodology=%t",
		permissions.Publish, permissions.Message, permissions.Attest, permissions.Merge, permissions.Methodology)
	if len(permissions.Spawn) > 0 {
		fmt.Fprintf(&b, " spawn=[%s]", strings.Join(permissions.Spawn, ", "))
	}
	b.WriteString("\n")
	if len(c.StandingOrders) > 0 {
		b.WriteString("Standing orders:\n")
		for _, order := range c.StandingOrders {
			order = strings.TrimSpace(order)
			if order == "" {
				continue
			}
			fmt.Fprintf(&b, "- %s\n", order)
		}
	}
	return b.String()
}

// ForAgent resolves an Agent's effective role, its ConfigFiles and its standing
// rules. When the Hive has no active methodology (domain.ErrNotFound) it uses
// Builtin. A nil source also selects Builtin.
//
// Timing: the returned ConfigFiles are meant for the next Cell Start, so a
// version change reaches the CLI's configuration directory only then. The
// returned StandingOrders and role description are rendered into every turn by
// the caller, so a newly active version applies to the very next prompt.
func ForAgent(ctx context.Context, source ActiveSource, agent domain.Agent) (AgentConfig, error) {
	var active domain.Methodology
	var files []domain.MethodologyFile
	builtin := false
	if source == nil {
		active.Manifest, files = Builtin()
		builtin = true
	} else {
		got, gotFiles, err := source.ActiveMethodology(ctx, agent.HiveID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			active.Manifest, files = Builtin()
			builtin = true
		case err != nil:
			return AgentConfig{}, err
		default:
			active, files = got, gotFiles
		}
	}

	roleName := strings.TrimSpace(agent.RoleName)
	if roleName == "" {
		roleName = active.Manifest.DefaultRole
	}
	role, err := Resolve(active.Manifest, roleName)
	if err != nil {
		return AgentConfig{}, err
	}
	configFiles := make(map[string]string, len(files))
	for _, file := range files {
		configFiles[ConfigPrefix+file.Path] = file.Content
	}
	return AgentConfig{
		Methodology:    active,
		RoleName:       roleName,
		Role:           role,
		ConfigFiles:    configFiles,
		StandingOrders: append([]string(nil), active.Manifest.StandingOrders...),
		Builtin:        builtin,
	}, nil
}

// Change is one append-only methodology history entry: who published or
// activated which version, when, and why.
type Change struct {
	ID        int64          `json:"id"`
	HiveID    string         `json:"hiveId"`
	Action    string         `json:"action"`
	Version   int            `json:"version"`
	Actor     domain.Address `json:"actor"`
	Note      string         `json:"note,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

func sortedRoleNames(roles map[string]domain.Role) []string {
	names := make([]string, 0, len(roles))
	for name := range roles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
