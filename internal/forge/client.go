// Package forge defines the hosting operations used by task orchestration.
// Wire field names retain their legacy spelling for persisted GitLab compatibility.
package forge

import (
	"context"
	"swarmjet/internal/domain"
	"time"
)

type IssueNote struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	System    bool      `json:"system"`
	Internal  bool      `json:"internal"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Author    struct {
		ID int64 `json:"id"`
	} `json:"author"`
}

type Branch struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
	Commit    struct {
		ID string `json:"id"`
	} `json:"commit"`
}

type Pipeline struct {
	ID     int64  `json:"id"`
	SHA    string `json:"sha"`
	Status string `json:"status"`
}

type MergeRequest struct {
	Description         string    `json:"description"`
	IID                 int64     `json:"iid"`
	URL                 string    `json:"web_url"`
	SourceBranch        string    `json:"source_branch"`
	TargetBranch        string    `json:"target_branch"`
	SHA                 string    `json:"sha"`
	State               string    `json:"state"`
	DetailedMergeStatus string    `json:"detailed_merge_status"`
	HasConflicts        bool      `json:"has_conflicts"`
	HeadPipeline        *Pipeline `json:"head_pipeline"`
}

type ProjectLabel struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description,omitempty"`
	Archived    bool   `json:"archived,omitempty"`
}

// Client operates within a single credential scope. Implementations must not
// expose credentials in values returned to orchestration or workflow history.
type Client interface {
	ListEligibleIssues(context.Context, int64) ([]domain.Issue, error)
	GetIssue(context.Context, int64, int64) (domain.Issue, error)
	Dependencies(context.Context, domain.Issue) ([]domain.IssueRef, error)
	ListIssueNotes(context.Context, domain.Issue) ([]IssueNote, error)
	GetBranch(context.Context, int64, string) (Branch, error)
	ListBranches(context.Context, int64) ([]Branch, error)
	EnsureTarget(context.Context, domain.Repository) (string, error)
	GetMR(context.Context, int64, int64) (MergeRequest, error)
	FindMR(context.Context, int64, string, string) (*MergeRequest, error)
	RequiredCIPassed(context.Context, int64, int64, string, bool) (bool, error)
	ClosingIssues(context.Context, int64, int64) ([]domain.IssueRef, error)
	UpdateLabels(context.Context, domain.Issue, []string, []string) error
	ListProjectLabels(context.Context, int64) ([]ProjectLabel, error)
	CreateProjectLabel(context.Context, int64, ProjectLabel) error
	UpsertProgress(context.Context, domain.Issue, string, string) error
	UpsertIssueReview(context.Context, domain.Issue, string, string, string) (string, error)
	CloseIssue(context.Context, domain.Issue) error
	Merge(context.Context, int64, int64, string, string) error
	UpdateMRDescription(context.Context, int64, int64, string) error
}
