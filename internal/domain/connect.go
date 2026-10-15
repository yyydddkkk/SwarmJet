package domain

import "time"

// Meta modes. Legacy projects carry an empty mode and behave like "imported".
const (
	MetaModeManaged  = "managed"
	MetaModeImported = "imported"
	// MetaModeDirect is a single code repository with no Meta-Repo. Used for
	// GitHub single-repo projects: Issue, changes, and PR all target that repo.
	MetaModeDirect = "direct"
)

// Connection is the persisted metadata for a hosting connection. The token
// itself is sealed outside this record and is never part of a Project, Task,
// log, event or Temporal history.
type Connection struct {
	Provider             Provider  `json:"provider,omitempty"`
	GitHubInstallationID int64     `json:"githubInstallationId,omitempty"`
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	GitLabURL            string    `json:"gitlabUrl"`
	Username             string    `json:"username"`
	Version              int       `json:"version"`
	HasMergeToken        bool      `json:"hasMergeToken"`
	Fingerprint          string    `json:"fingerprint"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

// ManagedMeta reports whether the Meta-Repo is created and owned by SwarmJet.
func (p Project) ManagedMeta() bool { return p.MetaMode == MetaModeManaged }

// DirectRepo reports whether the project uses a single code repository with no
// Meta-Repo. Direct projects do not run managed-Meta initialization.
func (p Project) DirectRepo() bool { return p.MetaMode == MetaModeDirect }

// UsesMetaRepo reports whether workspace preparation depends on a Meta-Repo
// (managed or imported). Direct single-repo projects return false.
func (p Project) UsesMetaRepo() bool { return !p.DirectRepo() }

// AutoMergeEnabled resolves the project-level switch. nil preserves the legacy
// enabled behavior so existing projects do not silently change after upgrade.
func (p Project) AutoMergeEnabled() bool { return p.AutoMerge == nil || *p.AutoMerge }

// Credential reference prefixes distinguish saved connections from deployment
// environment variable names. Only the reference is persisted in snapshots.
const (
	CredentialConnectionPrefix = "connection:"
	CredentialMergePrefix      = "connection-merge:"
)

func ConnectionCredentialRef(id string) string { return CredentialConnectionPrefix + id }

// ConnectionMergeCredentialRef selects the optional merge token of a connection.
// When the connection has no separate merge token the resolver falls back to the
// primary token; the console must not claim independent permission isolation.
func ConnectionMergeCredentialRef(id string) string { return CredentialMergePrefix + id }

func (p Project) CredentialReference() string {
	if p.ConnectionID != "" {
		return ConnectionCredentialRef(p.ConnectionID)
	}
	return p.CredentialRef
}

func (p Project) MergeCredentialReference() string {
	if p.MergeCredentialRef != "" {
		return p.MergeCredentialRef
	}
	if p.ConnectionID != "" {
		return ConnectionMergeCredentialRef(p.ConnectionID)
	}
	return p.CredentialRef
}

// Initialization statuses. "blocked" is retryable; only ready/cancelled are final.
const (
	InitPending    = "pending"
	InitRunning    = "running"
	InitNeedsInput = "needs_input"
	InitReady      = "ready"
	InitBlocked    = "blocked"
	InitCancelled  = "cancelled"
)

func InitTerminal(status string) bool { return status == InitReady || status == InitCancelled }

// InitQuestion is a concrete missing-information prompt shown on the project page.
type InitQuestion struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Answer   string `json:"answer,omitempty"`
	Answered bool   `json:"answered"`
}

// InitReport records a generated artifact and whether it was independently checked.
type InitReport struct {
	Path        string `json:"path"`
	Description string `json:"description"`
	Verified    bool   `json:"verified"`
}

// InitVerification is a build/test command actually executed in an isolated,
// writable copy of the pinned revisions. DryRun marks a command that was only
// planned, never claimed as a passing check.
type InitVerification struct {
	Name     string   `json:"name"`
	Command  []string `json:"command"`
	Passed   bool     `json:"passed"`
	ExitCode int      `json:"exitCode"`
	Report   string   `json:"report"`
	DryRun   bool     `json:"dryRun,omitempty"`
}

// Initialization is the durable, observable state of the managed-Meta setup job.
// It separates "repositories connected", "analysis complete" and "ready for
// development tasks" so the console never claims readiness it has not verified.
type Initialization struct {
	ExecutionConfig       AgentConfig        `json:"executionConfig,omitempty"`
	NodeID                string             `json:"nodeId,omitempty"`
	NodeConfigVersion     int                `json:"nodeConfigVersion,omitempty"`
	Image                 string             `json:"image,omitempty"`
	Network               NetworkVersion     `json:"network,omitempty"`
	Publication           *MetaPublication   `json:"publication,omitempty"`
	ID                    string             `json:"id"`
	ProjectID             string             `json:"projectId"`
	Status                string             `json:"status"`
	Stage                 string             `json:"stage"`
	Progress              int                `json:"progress"`
	Message               string             `json:"message"`
	RepositoriesConnected bool               `json:"repositoriesConnected"`
	AnalysisComplete      bool               `json:"analysisComplete"`
	ReadyForTasks         bool               `json:"readyForTasks"`
	Questions             []InitQuestion     `json:"questions"`
	Reports               []InitReport       `json:"reports"`
	Verification          []InitVerification `json:"verification"`
	MetaSHA               string             `json:"metaSha"`
	RepositorySHAs        map[string]string  `json:"repositoryShas"`
	ExecutionID           string             `json:"executionId"`
	SessionID             string             `json:"sessionId,omitempty"`
	Attempts              int                `json:"attempts"`
	Error                 string             `json:"error,omitempty"`
	// AgentStopped records that a cancelled job's live analysis was stopped, so
	// the worker only calls the backend once per cancellation.
	AgentStopped bool      `json:"agentStopped,omitempty"`
	Version      int       `json:"version"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// MetaPublication tracks optional publication independently of initialization.
type MetaPublication struct {
	Status        string `json:"status"`
	Mode          string `json:"mode"`
	NamespaceID   int64  `json:"namespaceId,omitempty"`
	Path          string `json:"path"`
	RemoteID      int64  `json:"remoteId,omitempty"`
	URL           string `json:"url,omitempty"`
	CloneURL      string `json:"cloneUrl,omitempty"`
	Ref           string `json:"ref"`
	SHA           string `json:"sha"`
	GitLabURL     string `json:"gitlabUrl"`
	CredentialRef string `json:"credentialRef"`
	RequestID     string `json:"requestId"`
	Error         string `json:"error,omitempty"`
}
