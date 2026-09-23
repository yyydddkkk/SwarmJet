package domain

import "testing"

func TestIssueAgentSelection(t *testing.T) {
	p := Project{Development: AgentConfig{Adapter: "opencode", Model: "provider/old", Image: "image", TimeoutSeconds: 300}, Review: AgentConfig{Adapter: "opencode", Model: "provider/review", Image: "image", TimeoutSeconds: 600}}
	for _, id := range []string{"opencode", "codex", "dst", "agy", "claude"} {
		t.Run(id, func(t *testing.T) {
			resolved, err := ResolveIssueAgent(p, Issue{Labels: []string{"autonomous", "agent:" + id}}, []string{id})
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Development.Adapter != id || resolved.Review.Adapter != id || resolved.Development.Model != "" || resolved.Review.Model != "" || !resolved.Development.UseCLIDefaults || !resolved.Review.UseCLIDefaults {
				t.Fatalf("wrong snapshot: %+v", resolved)
			}
			if resolved.Review.TimeoutSeconds != 600 || p.Development.Model != "provider/old" {
				t.Fatal("selection mutated project or lost role settings")
			}
		})
	}
	for _, labels := range [][]string{{"agent:unknown"}, {"agent:codex/gpt6"}, {"agent:codex", "agent:opencode"}, {"agent:"}} {
		if _, err := ResolveIssueAgent(p, Issue{Labels: labels}, []string{"opencode", "codex"}); err == nil {
			t.Fatalf("accepted %v", labels)
		}
	}
	if _, err := ResolveIssueAgent(p, Issue{}, nil); err == nil {
		t.Fatal("new installation implicitly enabled agents")
	}
	if result, err := ResolveIssueAgent(p, Issue{Labels: []string{"autonomous", "swarmjet:queued"}}, []string{"opencode"}); err != nil || result.Development.Adapter != "opencode" {
		t.Fatal(result, err)
	}
}
