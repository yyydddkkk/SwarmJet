package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology"
)

// methodologyTools are visible to every token. Reads are allowed for every
// token; publish and activate additionally check the calling Agent's role
// permission in the handler (a user token is always allowed).
var methodologyTools = map[string]bool{
	"get_methodology":              true,
	"list_methodology_versions":    true,
	"publish_methodology":          true,
	"activate_methodology":         true,
	"list_methodology_templates":   true,
	"install_methodology_template": true,
}

func (ts *toolset) registerMethodology(s *mcpsdk.Server) {
	ts.registerMethodologyTemplates(s)
	ro := func() *mcpsdk.ToolAnnotations { return &mcpsdk.ToolAnnotations{ReadOnlyHint: true} }

	addTool(ts, s, &mcpsdk.Tool{
		Name:        "get_methodology",
		Description: "Read a Hive's methodology: the manifest the platform enforces, the file list and source, plus the file contents when includeFiles is true. Omit version to read the active version; use the builtin fallback flag when the Hive has not published one. Read-only.",
		Annotations: ro(),
	}, ts.getMethodology)

	addTool(ts, s, &mcpsdk.Tool{
		Name:        "list_methodology_versions",
		Description: "List a Hive's methodology versions newest first, with the active version and the append-only publish/activate history (who, when, which version, note). Read-only.",
		Annotations: ro(),
	}, ts.listMethodologyVersions)

	addTool(ts, s, &mcpsdk.Tool{
		Name:        "publish_methodology",
		Description: "Publish a new methodology version from methodology.yaml text plus its files. The YAML is validated strictly (unknown fields, referenced roles and prompt files, path safety, 500 files / 2 MiB) and the new version becomes active immediately. Requires a user token, or an agent token whose methodology role has permissions.methodology. Records who published it and notifies the Hive.",
	}, ts.publishMethodology)

	addTool(ts, s, &mcpsdk.Tool{
		Name:        "activate_methodology",
		Description: "Switch a Hive to an existing methodology version, for example to roll back. Requires a user token, or an agent token whose methodology role has permissions.methodology. Records who activated it and notifies the Hive.",
	}, ts.activateMethodology)
}

type methodologyFileView struct {
	Path    string `json:"path"`
	Size    int    `json:"size"`
	Content string `json:"content,omitempty"`
}

type methodologyView struct {
	domain.Methodology
	Builtin bool                  `json:"builtin,omitempty"`
	Files   []methodologyFileView `json:"files"`
}

func viewMethodology(m domain.Methodology, files []domain.MethodologyFile, builtin, includeContent bool) methodologyView {
	view := methodologyView{
		Methodology: m,
		Builtin:     builtin,
		Files:       make([]methodologyFileView, 0, len(files)),
	}
	for _, file := range files {
		entry := methodologyFileView{Path: file.Path, Size: len(file.Content)}
		if includeContent {
			entry.Content = file.Content
		}
		view.Files = append(view.Files, entry)
	}
	return view
}

type getMethodologyInput struct {
	HiveID       string `json:"hiveId" jsonschema:"the Hive whose methodology to read"`
	Version      int    `json:"version,omitempty" jsonschema:"version to read; omit or 0 for the active version"`
	IncludeFiles bool   `json:"includeFiles,omitempty" jsonschema:"include each file's content; defaults to false to keep the response small"`
}

type getMethodologyOutput struct {
	Methodology methodologyView `json:"methodology"`
}

func (ts *toolset) getMethodology(ctx context.Context, _ *mcpsdk.CallToolRequest, in getMethodologyInput) (*mcpsdk.CallToolResult, getMethodologyOutput, error) {
	if in.HiveID == "" {
		return nil, getMethodologyOutput{}, fmt.Errorf("hiveId is required")
	}
	if in.Version < 0 {
		return nil, getMethodologyOutput{}, fmt.Errorf("version must not be negative")
	}
	if err := ts.requireHive(in.HiveID); err != nil {
		return nil, getMethodologyOutput{}, err
	}
	if in.Version > 0 {
		m, files, err := ts.backend.MethodologyVersion(ctx, in.HiveID, in.Version)
		if err != nil {
			return nil, getMethodologyOutput{}, err
		}
		return nil, getMethodologyOutput{Methodology: viewMethodology(m, files, false, in.IncludeFiles)}, nil
	}
	m, files, err := ts.backend.ActiveMethodology(ctx, in.HiveID)
	if errors.Is(err, domain.ErrNotFound) {
		// Only an existing Hive falls back to the builtin methodology.
		if _, hiveErr := ts.backend.Hive(ctx, in.HiveID); hiveErr != nil {
			return nil, getMethodologyOutput{}, hiveErr
		}
		manifest, builtinFiles := methodology.Builtin()
		return nil, getMethodologyOutput{Methodology: viewMethodology(
			domain.Methodology{HiveID: in.HiveID, Manifest: manifest}, builtinFiles, true, in.IncludeFiles)}, nil
	}
	if err != nil {
		return nil, getMethodologyOutput{}, err
	}
	return nil, getMethodologyOutput{Methodology: viewMethodology(m, files, false, in.IncludeFiles)}, nil
}

type listMethodologyVersionsInput struct {
	HiveID string `json:"hiveId" jsonschema:"the Hive whose methodology history to read"`
}

type listMethodologyVersionsOutput struct {
	HiveID        string               `json:"hiveId"`
	ActiveVersion int                  `json:"activeVersion,omitempty"`
	Versions      []domain.Methodology `json:"versions"`
	Changes       []methodology.Change `json:"changes"`
}

func (ts *toolset) listMethodologyVersions(ctx context.Context, _ *mcpsdk.CallToolRequest, in listMethodologyVersionsInput) (*mcpsdk.CallToolResult, listMethodologyVersionsOutput, error) {
	if in.HiveID == "" {
		return nil, listMethodologyVersionsOutput{}, fmt.Errorf("hiveId is required")
	}
	if err := ts.requireHive(in.HiveID); err != nil {
		return nil, listMethodologyVersionsOutput{}, err
	}
	versions, err := ts.backend.MethodologyVersions(ctx, in.HiveID)
	if err != nil {
		return nil, listMethodologyVersionsOutput{}, err
	}
	changes, err := ts.backend.MethodologyChanges(ctx, in.HiveID)
	if err != nil {
		return nil, listMethodologyVersionsOutput{}, err
	}
	out := listMethodologyVersionsOutput{
		HiveID:   in.HiveID,
		Versions: versions,
		Changes:  changes,
	}
	if out.Versions == nil {
		out.Versions = []domain.Methodology{}
	}
	if out.Changes == nil {
		out.Changes = []methodology.Change{}
	}
	for _, version := range versions {
		if version.Active {
			out.ActiveVersion = version.Version
			break
		}
	}
	return nil, out, nil
}

type publishMethodologyInput struct {
	HiveID   string                    `json:"hiveId" jsonschema:"the Hive to publish into"`
	Manifest string                    `json:"manifest" jsonschema:"methodology.yaml contents as YAML text"`
	Files    []domain.MethodologyFile  `json:"files,omitempty" jsonschema:"the other methodology files, by relative path"`
	Source   *domain.MethodologySource `json:"source,omitempty" jsonschema:"optional upstream git source"`
	Note     string                    `json:"note,omitempty" jsonschema:"why this version was published"`
}

type publishMethodologyOutput struct {
	Methodology methodologyView `json:"methodology"`
}

func (ts *toolset) publishMethodology(ctx context.Context, _ *mcpsdk.CallToolRequest, in publishMethodologyInput) (*mcpsdk.CallToolResult, publishMethodologyOutput, error) {
	if in.HiveID == "" {
		return nil, publishMethodologyOutput{}, fmt.Errorf("hiveId is required")
	}
	if strings.TrimSpace(in.Manifest) == "" {
		return nil, publishMethodologyOutput{}, fmt.Errorf("manifest is required")
	}
	if err := ts.requireMethodologyWrite(ctx, in.HiveID); err != nil {
		return nil, publishMethodologyOutput{}, err
	}
	manifest, err := methodology.Parse([]byte(in.Manifest))
	if err != nil {
		return nil, publishMethodologyOutput{}, err
	}
	if err = methodology.Validate(manifest, in.Files); err != nil {
		return nil, publishMethodologyOutput{}, err
	}
	if utf8.RuneCountInString(in.Note) > maxMethodologyNote {
		return nil, publishMethodologyOutput{}, fmt.Errorf("note is longer than %d characters", maxMethodologyNote)
	}
	published, err := ts.backend.PublishMethodology(ctx, domain.Methodology{
		HiveID:    in.HiveID,
		Source:    in.Source,
		Manifest:  manifest,
		Note:      in.Note,
		CreatedBy: ts.actor.address(),
	}, in.Files)
	if err != nil {
		return nil, publishMethodologyOutput{}, err
	}
	if err = ts.notifyMethodologyChange(ctx, in.HiveID, methodology.ActionPublish, published.Version, in.Note); err != nil {
		return nil, publishMethodologyOutput{}, fmt.Errorf("published version %d but could not notify the Hive: %w", published.Version, err)
	}
	return nil, publishMethodologyOutput{Methodology: viewMethodology(published, in.Files, false, false)}, nil
}

type activateMethodologyInput struct {
	HiveID  string `json:"hiveId" jsonschema:"the Hive whose active version to switch"`
	Version int    `json:"version" jsonschema:"the existing version to activate, for example an older one to roll back to"`
	Note    string `json:"note,omitempty" jsonschema:"why this version was activated"`
}

type activateMethodologyOutput struct {
	Methodology methodologyView `json:"methodology"`
}

func (ts *toolset) activateMethodology(ctx context.Context, _ *mcpsdk.CallToolRequest, in activateMethodologyInput) (*mcpsdk.CallToolResult, activateMethodologyOutput, error) {
	if in.HiveID == "" {
		return nil, activateMethodologyOutput{}, fmt.Errorf("hiveId is required")
	}
	if in.Version <= 0 {
		return nil, activateMethodologyOutput{}, fmt.Errorf("version must be a positive existing version")
	}
	if err := ts.requireMethodologyWrite(ctx, in.HiveID); err != nil {
		return nil, activateMethodologyOutput{}, err
	}
	if utf8.RuneCountInString(in.Note) > maxMethodologyNote {
		return nil, activateMethodologyOutput{}, fmt.Errorf("note is longer than %d characters", maxMethodologyNote)
	}
	active, err := ts.backend.ActivateMethodology(ctx, in.HiveID, in.Version, ts.actor.address(), in.Note)
	if err != nil {
		return nil, activateMethodologyOutput{}, err
	}
	if err = ts.notifyMethodologyChange(ctx, in.HiveID, methodology.ActionActivate, active.Version, in.Note); err != nil {
		return nil, activateMethodologyOutput{}, fmt.Errorf("activated version %d but could not notify the Hive: %w", active.Version, err)
	}
	_, files, err := ts.backend.MethodologyVersion(ctx, in.HiveID, active.Version)
	if err != nil {
		return nil, activateMethodologyOutput{}, err
	}
	return nil, activateMethodologyOutput{Methodology: viewMethodology(active, files, false, false)}, nil
}

// requireMethodologyWrite allows user tokens and agent tokens whose effective
// methodology role carries permissions.methodology. The active manifest is
// read on every call, so a role permission change applies immediately.
func (ts *toolset) requireMethodologyWrite(ctx context.Context, hiveID string) error {
	if ts.token.Scope != domain.TokenAgent {
		return nil
	}
	if ts.actorErr != nil {
		return ts.actorErr
	}
	if ts.actor.agent == nil {
		return fmt.Errorf("permission denied: agent token %s has no agent", ts.token.ID)
	}
	if err := ts.requireHive(hiveID); err != nil {
		return err
	}
	if ts.actor.agent.Role == domain.RoleQueen {
		return nil
	}
	manifest := domain.MethodologyManifest{}
	m, _, err := ts.backend.ActiveMethodology(ctx, hiveID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		manifest, _ = methodology.Builtin()
	case err != nil:
		return err
	default:
		manifest = m.Manifest
	}
	role, err := methodology.Resolve(manifest, ts.actor.agent.RoleName)
	if err != nil || !role.Permissions.Methodology {
		name := strings.TrimSpace(ts.actor.agent.RoleName)
		if name == "" {
			name = manifest.DefaultRole
		}
		return fmt.Errorf("permission denied: methodology role %q may not publish or activate a methodology", name)
	}
	return nil
}

// notifyMethodologyChange appends one system message to the Hive Inbox so the
// change is visible alongside the rest of the swarm's conversation.
func (ts *toolset) notifyMethodologyChange(ctx context.Context, hiveID, action string, version int, note string) error {
	body := fmt.Sprintf("methodology %s: version %d by %s", action, version, ts.actor.address())
	if note = strings.TrimSpace(note); note != "" {
		body += ": " + note
	}
	_, err := ts.backend.SendMessage(ctx, domain.Message{
		HiveID: hiveID,
		From:   domain.AddressSystem,
		To:     domain.AddressHive,
		Kind:   domain.MessageEvent,
		Body:   truncateField(body, domain.MaxMessageBody),
		Trust:  domain.TrustSystem,
	})
	return err
}

const maxMethodologyNote = 2000
