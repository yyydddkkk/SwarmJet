package domain

// Repository is one code repository attached to a Hive or project snapshot.
// Only the fields still consumed by the GitHub client and Cell runtime remain.
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

// AgentConfig carries the image, network and resource limits the Cell runtime
// applies to CLI containers.
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

// IssueRef identifies an issue on a hosting platform.
type IssueRef struct {
	Provider  Provider `json:"provider,omitempty"`
	ProjectID int64    `json:"projectId"`
	IID       int64    `json:"iid"`
}

// Issue is the GitHub issue shape the PR client consumes.
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
