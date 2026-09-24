// Package labels owns the GitLab label names, colors and reconciliation rules.
package labels

import (
	"context"
	"fmt"
	"strings"

	"swarmjet/internal/forge"
	"swarmjet/internal/gitlab"
)

type Status struct {
	Name          string `json:"name"`
	Color         string `json:"color"`
	ExistingColor string `json:"existingColor,omitempty"`
	Exists        bool   `json:"exists"`
	Archived      bool   `json:"archived,omitempty"`
}

func Selection(enabled []string) []gitlab.ProjectLabel {
	colors := map[string]string{
		"opencode": "#16A34A", "codex": "#7C3AED", "dst": "#D97706",
		"agy": "#DB2777", "claude": "#B45309",
	}
	wanted := []gitlab.ProjectLabel{{Name: "autonomous", Color: "#2563EB", Description: "Swarmjet 接单入口"}}
	for _, id := range enabled {
		if color, ok := colors[id]; ok {
			wanted = append(wanted, gitlab.ProjectLabel{Name: "agent:" + id, Color: color, Description: "指定 Swarmjet 使用 " + id + " 执行"})
		}
	}
	return wanted
}

func System(stage string, clarification bool) []gitlab.ProjectLabel {
	color := "#64748B"
	switch stage {
	case "blocked", "partial_merge", "cancelled":
		color = "#DC2626"
	case "waiting_approval", "waiting_manual_merge":
		color = "#D97706"
	case "completed":
		color = "#16A34A"
	case "developing", "publishing", "reviewing", "verifying", "merging":
		color = "#2563EB"
	}
	wanted := []gitlab.ProjectLabel{{Name: "swarmjet:" + stage, Color: color, Description: "Swarmjet 任务阶段"}}
	if clarification {
		wanted = append(wanted, gitlab.ProjectLabel{Name: "needs-clarification", Color: "#D97706", Description: "任务存在待澄清假设"})
	}
	return wanted
}

func Plan(ctx context.Context, client forge.Client, projectID int64, wanted []gitlab.ProjectLabel) ([]Status, error) {
	if len(wanted) == 0 {
		return []Status{}, nil
	}
	current, err := client.ListProjectLabels(ctx, projectID)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]gitlab.ProjectLabel, len(current))
	for _, label := range current {
		byName[strings.ToLower(label.Name)] = label
	}
	result := make([]Status, 0, len(wanted))
	for _, label := range wanted {
		status := Status{Name: label.Name, Color: label.Color}
		if existing, ok := byName[strings.ToLower(label.Name)]; ok {
			status.Exists, status.ExistingColor = true, existing.Color
			status.Archived = existing.Archived
		}
		result = append(result, status)
	}
	return result, nil
}

// Ensure only creates absent labels. It never changes or removes a user's labels.
func Ensure(ctx context.Context, client forge.Client, projectID int64, wanted []gitlab.ProjectLabel) ([]Status, error) {
	status, err := Plan(ctx, client, projectID, wanted)
	if err != nil {
		return nil, err
	}
	for i, item := range status {
		if item.Exists {
			continue
		}
		if err := client.CreateProjectLabel(ctx, projectID, wanted[i]); err != nil {
			// GitLab versions differ in their duplicate-label response. Accept a
			// concurrent creation only after verifying the name now exists.
			fresh, readErr := Plan(ctx, client, projectID, wanted[i:i+1])
			if readErr != nil || !fresh[0].Exists {
				return status, fmt.Errorf("create label %q: %w", item.Name, err)
			}
			status[i].ExistingColor = fresh[0].ExistingColor
		} else {
			status[i].ExistingColor = item.Color
		}
		status[i].Exists = true
	}
	return status, nil
}
