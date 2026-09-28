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
	DockerNetwork  string  `json:"dockerNetwork,omitempty"`
	MemoryMB       int     `json:"memoryMB,omitempty"`
	CPUs           float64 `json:"cpus,omitempty"`
	PidsLimit      int     `json:"pidsLimit,omitempty"`
	UseCLIDefaults bool    `json:"useCliDefaults,omitempty"`
	Adapter        string  `json:"adapter"`
	Model          string  `json:"model"`
	Image          string  `json:"image"`
	TimeoutSeconds int     `json:"timeoutSeconds"`
}
type Check struct {
	Name     string   `json:"name" yaml:"name"`
	Service  string   `json:"service" yaml:"service"`
	Command  []string `json:"command" yaml:"command"`
	Required bool     `json:"required" yaml:"required"`
}
type AcceptanceCommand struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Argv     []string `json:"argv"`
	Evidence string   `json:"evidence"`
}
type ManualStep struct {
	Action   string `json:"action"`
	Expected string `json:"expected"`
}
type ManualAcceptance struct {
	Prerequisites []string     `json:"prerequisites"`
	Steps         []ManualStep `json:"steps"`
	Notes         []string     `json:"notes"`
}
type IntegrationConfig struct {
	Mode                  string              `json:"mode,omitempty"`
	Image                 string              `json:"image,omitempty"`
	Commands              []AcceptanceCommand `json:"commands,omitempty"`
	ComposeFile           string              `json:"composeFile"`
	Checks                []Check             `json:"checks"`
	StartupTimeoutSeconds int                 `json:"startupTimeoutSeconds"`
}
type Project struct {
	Provider           Provider      `json:"provider,omitempty"`
	MergeMethod        string        `json:"mergeMethod,omitempty"`
	Nodes              NodeSelection `json:"nodes,omitempty"`
	AcceptanceStrategy string        `json:"acceptanceStrategy,omitempty"`
	// Empty reviewMode retains independent review for legacy projects.
	ReviewMode         string `json:"reviewMode,omitempty"`
	ID                 string `json:"id"`
	Name               string `json:"name"`
	MetaURL            string `json:"metaUrl"`
	MetaRef            string `json:"metaRef"`
	Version            int    `json:"version"`
	Enabled            bool   `json:"enabled"`
	GitLabURL          string `json:"gitlabUrl"`
	CredentialRef      string `json:"credentialRef"`
	MergeCredentialRef string `json:"mergeCredentialRef"`
	// ConnectionID references a saved hosting connection. Its token is stored
	// encrypted outside the project snapshot; only the reference is persisted.
	ConnectionID string `json:"connectionId,omitempty"`
	// MetaMode is "managed" for a system-created Meta-Repo, "imported" (or empty
	// for legacy projects) when MetaURL points at an existing remote repository.
	MetaMode string `json:"metaMode,omitempty"`
	// AutoMerge is a project-level switch. A nil value means the legacy default
	// (enabled) so existing projects keep their behavior across upgrades.
	AutoMerge *bool `json:"autoMerge,omitempty"`
	// InitStatus tracks the managed-Meta initialization funnel.
	InitStatus   string            `json:"initStatus,omitempty"`
	Repositories []Repository      `json:"repositories"`
	Development  AgentConfig       `json:"development"`
	Review       AgentConfig       `json:"review"`
	Integration  IntegrationConfig `json:"integration"`
	MaxReworks   int               `json:"maxReworks"`
	RequireCI    bool              `json:"requireCI"`
}

func (p Project) IndependentReview() bool { return p.ReviewMode != "none" }

type IssueRef struct {
	Provider  Provider `json:"provider,omitempty"`
	ProjectID int64    `json:"projectId"`
	IID       int64    `json:"iid"`
}
type Issue struct {
	Provider    Provider `json:"provider,omitempty"`
	ProjectID   int64    `json:"projectId"`
	IID         int64    `json:"iid"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	URL         string   `json:"url"`
	State       string   `json:"state"`
	Labels      []string `json:"labels"`
}
type Revision struct {
	// MergeQueueEntryID records a GitHub queue entry until the PR actually merges.
	MergeQueueEntryID string   `json:"mergeQueueEntryId,omitempty"`
	RepositoryID      string   `json:"repositoryId"`
	ProjectID         int64    `json:"projectId"`
	Path              string   `json:"path"`
	BaseSHA           string   `json:"baseSha"`
	InitialSHA        string   `json:"initialSha"`
	HeadSHA           string   `json:"headSha"`
	TargetBranch      string   `json:"targetBranch"`
	Branch            string   `json:"branch"`
	Changed           bool     `json:"changed"`
	Sensitive         bool     `json:"sensitive"`
	Files             []string `json:"files"`
	MRIID             int64    `json:"mrIid"`
	MRURL             string   `json:"mrUrl"`
	Merged            bool     `json:"merged"`
}
type Workspace struct {
	TaskID    string     `json:"taskId"`
	Root      string     `json:"root"`
	MetaSHA   string     `json:"metaSha"`
	Revisions []Revision `json:"revisions"`
}
type Verification struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	Passed   bool   `json:"passed"`
	// EnvironmentFailure means the check could not run reliably, so its failure
	// must not be treated as evidence that the task code needs rework.
	EnvironmentFailure bool   `json:"environmentFailure,omitempty"`
	ExitCode           int    `json:"exitCode"`
	Report             string `json:"report"`
	RevisionDigest     string `json:"revisionDigest"`
}
type Finding struct {
	RepositoryID string `json:"repositoryId"`
	Path         string `json:"path"`
	Message      string `json:"message"`
}
type AgentResult struct {
	AcceptanceCommands []AcceptanceCommand `json:"acceptanceCommands,omitempty"`
	HumanAcceptance    *ManualAcceptance   `json:"humanAcceptance,omitempty"`
	Outcome            string              `json:"outcome"`
	Summary            string              `json:"summary"`
	Assumptions        []string            `json:"assumptions"`
	Findings           []Finding           `json:"findings"`
	NeedsHelp          string              `json:"needsHelp"`
	// Questions carries concrete missing-information prompts for the init role.
	Questions []string `json:"questions,omitempty"`
}
type Execution struct {
	ID            string        `json:"id"`
	SessionID     string        `json:"sessionId"`
	ContainerID   string        `json:"containerId"`
	Status        string        `json:"status"`
	ExitCode      int           `json:"exitCode"`
	LogPath       string        `json:"logPath"`
	FailureReason string        `json:"failureReason,omitempty"`
	FailureCode   string        `json:"failureCode,omitempty"`
	ResultRepair  bool          `json:"resultRepair,omitempty"`
	Result        *AgentResult  `json:"result,omitempty"`
	Artifacts     []ArtifactRef `json:"artifacts,omitempty"`
}

// ArtifactRef locates verified output independently of a Runner Pod or PVC.
type ArtifactRef struct {
	TaskID      string `json:"taskId"`
	ExecutionID string `json:"executionId"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}
type AgentRequest struct {
	ExecutionID string `json:"executionId"`
	TaskID      string `json:"taskId"`
	Role        string `json:"role"`
	Workspace   string `json:"workspace"`
	Prompt      string `json:"prompt"`
	SessionID   string `json:"sessionId"`
	// ProfileScope reuses the development home for a merge without review.
	ProfileScope string `json:"profileScope,omitempty"`
	// ReadOnlyWorkspace permits result recovery without changing task sources.
	ReadOnlyWorkspace bool        `json:"readOnlyWorkspace,omitempty"`
	Config            AgentConfig `json:"config"`
	// GeneratedDir is the only writable directory for an init analysis. It is a
	// backend-local path and is never serialized into workflow history.
	GeneratedDir string            `json:"-"`
	Env          map[string]string `json:"-"`
}
type Task struct {
	Round                int                 `json:"round,omitempty"`
	PreviousTaskID       string              `json:"previousTaskId,omitempty"`
	TriggerNoteIDs       []int64             `json:"triggerNoteIds,omitempty"`
	TriggerText          string              `json:"triggerText,omitempty"`
	DependencyTasks      map[string]string   `json:"dependencyTasks,omitempty"`
	Network              NetworkVersion      `json:"network,omitempty"`
	NodeConfigVersion    int                 `json:"nodeConfigVersion,omitempty"`
	AcceptanceCommands   []AcceptanceCommand `json:"acceptanceCommands,omitempty"`
	HumanAcceptance      *ManualAcceptance   `json:"humanAcceptance,omitempty"`
	AcceptanceIncomplete bool                `json:"acceptanceIncomplete,omitempty"`
	Checkpoint           *WorkflowCheckpoint `json:"checkpoint,omitempty"`
	ID                   string              `json:"id"`
	// TraceParent carries only the W3C trace context across the durable command
	// outbox. It contains no task input, credentials, or repository metadata.
	TraceParent      string          `json:"traceParent,omitempty"`
	NodeID           string          `json:"nodeId,omitempty"`
	ExecutionPool    string          `json:"executionPool,omitempty"`
	RunnerResources  RunnerResources `json:"runnerResources,omitempty"`
	RunnerImage      string          `json:"runnerImage,omitempty"`
	ExecutionID      string          `json:"executionId,omitempty"`
	AuthorityVersion int64           `json:"authorityVersion,omitempty"`
	AdmissionStatus  string          `json:"admissionStatus,omitempty"`
	AdmissionBackend string          `json:"admissionBackend,omitempty"`
	Project          Project         `json:"project"`
	// nil inherits the project's snapshot; an explicit value controls this task.
	AutoMergeOverride *bool                `json:"autoMergeOverride,omitempty"`
	Issue             Issue                `json:"issue"`
	Dependencies      []IssueRef           `json:"dependencies"`
	Stage             string               `json:"stage"`
	Message           string               `json:"message"`
	Workspace         Workspace            `json:"workspace"`
	Executions        map[string]Execution `json:"executions"`
	Verification      []Verification       `json:"verification"`
	Assumptions       []string             `json:"assumptions"`
	Notes             []string             `json:"notes"`
	Reworks           int                  `json:"reworks"`
	ReviewDigest      string               `json:"reviewDigest"`
	ApprovalDigest    string               `json:"approvalDigest"`
	Paused            bool                 `json:"paused"`
	CreatedAt         time.Time            `json:"createdAt"`
	UpdatedAt         time.Time            `json:"updatedAt"`
}

// RunnerResources is frozen at task creation so later pool configuration
// changes cannot silently alter an admitted execution.
type RunnerResources struct {
	CPUMilli       int `json:"cpuMilli"`
	MemoryMi       int `json:"memoryMi"`
	StorageMi      int `json:"storageMi"`
	TimeoutSeconds int `json:"timeoutSeconds"`
}

func (t Task) AutoMergeEnabled() bool {
	if t.AutoMergeOverride != nil {
		return *t.AutoMergeOverride
	}
	return t.Project.AutoMergeEnabled()
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
	AutoMerge    *bool      `json:"autoMerge,omitempty"`
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
	Readiness NodeReadiness `json:"readiness"`
	ID        string        `json:"id"`
	Address   string        `json:"address"`
	Capacity  int           `json:"capacity"`
	Version   string        `json:"version"`
	LastSeen  time.Time     `json:"lastSeen"`
}

// NodeStatus combines a node with its current load for the console and the
// scheduler.
type NodeStatus struct {
	Config  *NodeConfig `json:"config,omitempty"`
	Node    Node        `json:"node"`
	Running int         `json:"running"`
	Online  bool        `json:"online"`
}

// NodeOnlineAfter is how recently a node must have heartbeated to receive work.
const NodeOnlineAfter = 30 * time.Second
