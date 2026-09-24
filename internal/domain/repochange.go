package domain

import "time"

// RepositoryChange records one requested addition until its Meta manifest and
// project registration agree. A remote change is applied only after its MR is
// merged; local managed changes are committed before project registration.
type RepositoryChange struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"projectId"`
	Repository      Repository `json:"repository"`
	Status          string     `json:"status"`
	MetaProjectID   int64      `json:"metaProjectId,omitempty"`
	MetaFile        string     `json:"metaFile,omitempty"`
	SourceBranch    string     `json:"sourceBranch,omitempty"`
	MergeRequestID  int64      `json:"mergeRequestId,omitempty"`
	MergeRequestURL string     `json:"mergeRequestUrl,omitempty"`
	Error           string     `json:"error,omitempty"`
	DocsStale       bool       `json:"docsStale,omitempty"`
	Attempts        int        `json:"attempts,omitempty"`
	Version         int        `json:"version"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}
