package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology/catalog"
)

func (ts *toolset) registerMethodologyTemplates(s *mcpsdk.Server) {
	addTool(ts, s, &mcpsdk.Tool{Name: "list_methodology_templates", Description: "List the optional built-in methodology templates. Listing does not install a template.", Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true}}, ts.listMethodologyTemplates)
	addTool(ts, s, &mcpsdk.Tool{Name: "install_methodology_template", Description: "Publish and immediately activate a built-in template as a new methodology version. Requires a user token or the Hive's Queen; install only after the user chooses a template."}, ts.installMethodologyTemplate)
}

type listMethodologyTemplatesInput struct{}
type listMethodologyTemplatesOutput struct {
	Templates []domain.MethodologyTemplate `json:"templates"`
}

func (ts *toolset) listMethodologyTemplates(context.Context, *mcpsdk.CallToolRequest, listMethodologyTemplatesInput) (*mcpsdk.CallToolResult, listMethodologyTemplatesOutput, error) {
	templates, err := catalog.List()
	return nil, listMethodologyTemplatesOutput{Templates: templates}, err
}

type installMethodologyTemplateInput struct {
	HiveID string `json:"hiveId" jsonschema:"the Hive to install into"`
	Name   string `json:"name" jsonschema:"the built-in template name"`
	Note   string `json:"note,omitempty" jsonschema:"why this template was installed"`
}

func (ts *toolset) installMethodologyTemplate(ctx context.Context, req *mcpsdk.CallToolRequest, in installMethodologyTemplateInput) (*mcpsdk.CallToolResult, publishMethodologyOutput, error) {
	if ts.token.Scope == domain.TokenAgent {
		if ts.actorErr != nil {
			return nil, publishMethodologyOutput{}, ts.actorErr
		}
		if ts.actor.agent == nil || ts.actor.agent.Role != domain.RoleQueen {
			return nil, publishMethodologyOutput{}, fmt.Errorf("permission denied: installing a template requires a user token or Queen")
		}
		if err := ts.requireHive(in.HiveID); err != nil {
			return nil, publishMethodologyOutput{}, err
		}
	}
	template, manifest, files, err := catalog.Get(in.Name)
	if err != nil {
		return nil, publishMethodologyOutput{}, err
	}
	// JSON is valid YAML and preserves the domain contract's field names.
	body, err := json.Marshal(manifest)
	if err != nil {
		return nil, publishMethodologyOutput{}, err
	}
	return ts.publishMethodology(ctx, req, publishMethodologyInput{
		HiveID: in.HiveID, Manifest: string(body), Files: files, Note: in.Note,
		Source: &domain.MethodologySource{URL: "builtin:" + template.Name, Ref: template.Version},
	})
}
