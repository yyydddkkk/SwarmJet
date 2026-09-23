package execution

import (
	"context"
	"fmt"
	"swarmjet/internal/domain"
)

// AnalysisParams is an authenticated transient RPC payload, not workflow state.
type AnalysisParams struct {
	Initialization domain.Initialization `json:"initialization"`
	Project        domain.Project        `json:"project"`
	Env            map[string]string     `json:"env"`
}
type AnalysisResult struct {
	Execution domain.Execution    `json:"execution"`
	Reports   []domain.InitReport `json:"reports,omitempty"`
	Files     map[string]string   `json:"files,omitempty"`
}

func (n *RemoteNode) Analyze(ctx context.Context, p AnalysisParams) (AnalysisResult, error) {
	var out AnalysisResult
	e := n.call(ctx, "staticAnalysis", p, &out)
	return out, e
}
func (n *LocalNode) Analyze(ctx context.Context, p AnalysisParams) (AnalysisResult, error) {
	if n.StaticAnalysis == nil {
		return AnalysisResult{}, fmt.Errorf("节点不支持静态初始化")
	}
	return n.StaticAnalysis(ctx, p)
}
