// Package execution defines the agent lifecycle used by orchestration, without
// exposing Docker commands or requiring callers to prepare backend-local files.
package execution

import (
	"context"

	"swarmjet/internal/domain"
)

// AgentBackend owns execution records, sessions, launch configuration and results.
// Start must adopt an existing execution on retries with the same execution ID;
// a nonempty SessionID resumes that exact session. Requests contain credentials
// and must not be persisted or logged wholesale by an implementation.
//
// Inspect returns observed execution state, including a validated result on
// success. Cancel returns nil only after execution has stopped. CancelTask stops
// every live execution belonging to the task, including prior rework attempts.
// TaskExecutions reads backend-owned records, not task-supplied result artifacts.
//
// Workspace and LogPath currently refer to paths on the local execution host.
// This interface alone does not provide remote workspace placement or recovery.
type AgentBackend interface {
	Start(context.Context, domain.AgentRequest) (domain.Execution, error)
	Inspect(context.Context, string) (domain.Execution, error)
	Cancel(context.Context, string) error
	CancelTask(context.Context, string) error
	TaskExecutions(context.Context, string) ([]domain.Execution, error)
}
