package domain

import (
	"fmt"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/agentcli"
)

func ValidateEnabledAgents(ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if _, ok := agentcli.Lookup(id); !ok || seen[id] {
			return fmt.Errorf("unknown or duplicate agent %q", id)
		}
		seen[id] = true
	}
	return nil
}

// ResolveIssueAgent freezes the label choice into a task's project snapshot.
// Project configuration and existing task snapshots are never mutated.
func ResolveIssueAgent(p Project, issue Issue, enabled []string) (Project, error) {
	id, labels := p.Development.Adapter, 0
	for _, label := range issue.Labels {
		if strings.HasPrefix(label, "agent:") {
			labels++
			id = strings.TrimPrefix(label, "agent:")
		}
	}
	if labels > 1 {
		return p, fmt.Errorf("只能指定一个 agent: 标签，请移除冲突标签")
	}
	if _, ok := agentcli.Lookup(id); !ok {
		return p, fmt.Errorf("不支持 Agent %q；请选择 opencode、codex、dst、agy 或 claude", id)
	}
	allowed := false
	for _, v := range enabled {
		allowed = allowed || v == id
	}
	if !allowed {
		return p, fmt.Errorf("Agent %q 尚未在执行器环境中启用", id)
	}
	p.Development.Adapter, p.Review.Adapter = id, id
	p.Development.Model, p.Review.Model = "", ""
	p.Development.UseCLIDefaults, p.Review.UseCLIDefaults = true, true
	return p, nil
}
