package domain

import "time"

type Repository struct {
	ID             string   `json:"id" yaml:"id"`
	ProjectID      int64    `json:"projectId" yaml:"projectId"`
	URL            string   `json:"url" yaml:"url"`
	Path           string   `json:"path" yaml:"path"`
	TargetBranch   string   `json:"targetBranch" yaml:"targetBranch"`
	CreateFrom     string   `json:"createFrom,omitempty" yaml:"createFrom,omitempty"`
	DependsOn      []string `json:"dependsOn" yaml:"dependsOn"`
	SensitivePaths []string `json:"sensitivePaths" yaml:"sensitivePaths"`
}
type AgentConfig struct {
	Adapter        string `json:"adapter"`
	Model          string `json:"model"`
	Image          string `json:"image"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}
type Check struct {
	Name     string   `json:"name" yaml:"name"`
	Service  string   `json:"service" yaml:"service"`
	Command  []string `json:"command" yaml:"command"`
	Required bool     `json:"required" yaml:"required"`
}
type IntegrationConfig struct {
	ComposeFile           string  `json:"composeFile"`
	Checks                []Check `json:"checks"`
	StartupTimeoutSeconds int     `json:"startupTimeoutSeconds"`
}
type Project struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	MetaURL            string            `json:"metaUrl"`
	MetaRef            string            `json:"metaRef"`
	Version            int               `json:"version"`
	Enabled            bool              `json:"enabled"`
	GitLabURL          string            `json:"gitlabUrl"`
	CredentialRef      string            `json:"credentialRef"`
	MergeCredentialRef string            `json:"mergeCredentialRef"`
	Repositories       []Repository      `json:"repositories"`
	Development        AgentConfig       `json:"development"`
	Review             AgentConfig       `json:"review"`
	Integration        IntegrationConfig `json:"integration"`
	MaxReworks         int               `json:"maxReworks"`
	RequireCI          bool              `json:"requireCI"`
}
type IssueRef struct {
	ProjectID int64 `json:"projectId"`
	IID       int64 `json:"iid"`
}
type Issue struct {
	ProjectID   int64    `json:"projectId"`
	IID         int64    `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
	State       string   `json:"state"`
	Labels      []string `json:"labels"`
}
type Revision struct {
	RepositoryID string   `json:"repositoryId"`
	ProjectID    int64    `json:"projectId"`
	Path         string   `json:"path"`
	BaseSHA      string   `json:"baseSha"`
	InitialSHA   string   `json:"initialSha"`
	HeadSHA      string   `json:"headSha"`
	TargetBranch string   `json:"targetBranch"`
	Branch       string   `json:"branch"`
	Changed      bool     `json:"changed"`
	Sensitive    bool     `json:"sensitive"`
	Files        []string `json:"files"`
	MRIID        int64    `json:"mrIid"`
	MRURL        string   `json:"mrUrl"`
	Merged       bool     `json:"merged"`
}
type Workspace struct {
	TaskID    string     `json:"taskId"`
	Root      string     `json:"root"`
	MetaSHA   string     `json:"metaSha"`
	Revisions []Revision `json:"revisions"`
}
type Verification struct {
	Name           string `json:"name"`
	Required       bool   `json:"required"`
	Passed         bool   `json:"passed"`
	ExitCode       int    `json:"exitCode"`
	Report         string `json:"report"`
	RevisionDigest string `json:"revisionDigest"`
}
type Finding struct {
	RepositoryID string `json:"repositoryId"`
	Path         string `json:"path"`
	Message      string `json:"message"`
}
type AgentResult struct {
	Outcome     string    `json:"outcome"`
	Summary     string    `json:"summary"`
	Assumptions []string  `json:"assumptions"`
	Findings    []Finding `json:"findings"`
	NeedsHelp   string    `json:"needsHelp"`
}
type Execution struct {
	ID            string       `json:"id"`
	SessionID     string       `json:"sessionId"`
	ContainerID   string       `json:"containerId"`
	Status        string       `json:"status"`
	ExitCode      int          `json:"exitCode"`
	LogPath       string       `json:"logPath"`
	FailureReason string       `json:"failureReason,omitempty"`
	Result        *AgentResult `json:"result,omitempty"`
}
type AgentRequest struct {
	ExecutionID string            `json:"executionId"`
	TaskID      string            `json:"taskId"`
	Role        string            `json:"role"`
	Workspace   string            `json:"workspace"`
	Prompt      string            `json:"prompt"`
	SessionID   string            `json:"sessionId"`
	Config      AgentConfig       `json:"config"`
	Env         map[string]string `json:"-"`
}
type Task struct {
	Checkpoint     *WorkflowCheckpoint  `json:"checkpoint,omitempty"`
	ID             string               `json:"id"`
	NodeID         string               `json:"nodeId,omitempty"`
	Project        Project              `json:"project"`
	Issue          Issue                `json:"issue"`
	Dependencies   []IssueRef           `json:"dependencies"`
	Stage          string               `json:"stage"`
	Message        string               `json:"message"`
	Workspace      Workspace            `json:"workspace"`
	Executions     map[string]Execution `json:"executions"`
	Verification   []Verification       `json:"verification"`
	Assumptions    []string             `json:"assumptions"`
	Notes          []string             `json:"notes"`
	Reworks        int                  `json:"reworks"`
	ReviewDigest   string               `json:"reviewDigest"`
	ApprovalDigest string               `json:"approvalDigest"`
	Paused         bool                 `json:"paused"`
	CreatedAt      time.Time            `json:"createdAt"`
	UpdatedAt      time.Time            `json:"updatedAt"`
}
type WorkflowCheckpoint struct {
	Serial        int             `json:"serial"`
	PreviousPhase string          `json:"previousPhase"`
	Seen          map[string]bool `json:"seen"`
	Retry         bool            `json:"retry"`
	Rejected      bool            `json:"rejected"`
	Cancelled     bool            `json:"cancelled"`
	CIWaits       int             `json:"ciWaits"`
}
type Command struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind"`
	Text         string     `json:"text"`
	Digest       string     `json:"digest"`
	Dependencies []IssueRef `json:"dependencies,omitempty"`
}
type Event struct {
	ID        int64     `json:"id"`
	TaskID    string    `json:"taskId"`
	Kind      string    `json:"kind"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"createdAt"`
}

// Node is one registered execution host. Running work is derived from tasks
// pinned to the node; LastSeen drives liveness.
type Node struct {
	ID       string    `json:"id"`
	Address  string    `json:"address"`
	Capacity int       `json:"capacity"`
	Version  string    `json:"version"`
	LastSeen time.Time `json:"lastSeen"`
}

// NodeStatus combines a node with its current load for the console and the
// scheduler.
type NodeStatus struct {
	Node    Node `json:"node"`
	Running int  `json:"running"`
	Online  bool `json:"online"`
}

// NodeOnlineAfter is how recently a node must have heartbeated to receive work.
const NodeOnlineAfter = 30 * time.Second
