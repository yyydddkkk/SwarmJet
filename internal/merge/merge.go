// Package merge decides whether a pull request is allowed to merge and, when a
// Hive opts in, performs the merge with the GitHub App identity.
//
// The rules are the round-4 contract: the PR must be open and not a draft, it
// must target this Hive's repository, the latest un-superseded verdict must be
// PASS or PASS+NOTES for the current head, every required check must be green,
// and GitHub must not report a conflict. Nothing here bypasses GitHub's own
// branch protection or approval requirements: a rejected merge is reported.
package merge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
)

// VerdictReader is the round-4 storage contract. *store.Store implements it
// once the data-layer task lands; tests use a fake.
type VerdictReader interface {
	LatestVerdict(ctx context.Context, hiveID string, pr int64) (domain.Verdict, error)
	Verdicts(ctx context.Context, hiveID string, pr int64) ([]domain.Verdict, error)
}

// Store is the subset of the Hive store the gate and scheduler need. It only
// uses signatures that already exist in round 3, so this package builds before
// the verdict tables land.
type Store interface {
	Hive(ctx context.Context, id string) (domain.Hive, error)
	Agent(ctx context.Context, id string) (domain.Agent, error)
	ForagerByPR(ctx context.Context, hiveID string, pr int64) (domain.Agent, error)
	Inbox(ctx context.Context, hiveID string, to domain.Address, afterID string, limit int) ([]domain.Message, error)
	SendMessage(ctx context.Context, m domain.Message) (domain.Message, error)
}

// GitHub is the read/merge surface the gate needs. github.Client implements it.
type GitHub interface {
	RepositoryID(ctx context.Context, fullName string) (int64, error)
	GetPullRequest(ctx context.Context, id, number int64) (github.PullRequest, error)
	RequiredChecks(ctx context.Context, id, number int64, sha string) (github.CheckResult, error)
	CheckRuns(ctx context.Context, id int64, sha string) ([]github.CheckRun, error)
	RequestMerge(ctx context.Context, id, number int64, sha, method string) (github.MergeResult, error)
	GetMergeState(ctx context.Context, id, number int64, expectedSHA string) (github.MergeResult, error)
}

// GitHubSource returns a client scoped to one repository. The production source
// mints a repository-scoped installation token; tests use StaticSource.
type GitHubSource interface {
	GitHub(ctx context.Context, repository string) (GitHub, error)
}

// StaticSource always returns the same client. It is useful for tests and for
// callers that already resolved the repository token.
type StaticSource struct {
	Client GitHub
}

func (s StaticSource) GitHub(context.Context, string) (GitHub, error) {
	if s.Client == nil {
		return nil, errors.New("GitHub client is not configured")
	}
	return s.Client, nil
}

// Check is one GitHub check run in a readiness result.
type Check struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url,omitempty"`
	Output     string `json:"output,omitempty"`
}

// Pull is the PR state used by the gate.
type Pull struct {
	Number         int64  `json:"number"`
	State          string `json:"state"`
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	URL            string `json:"url,omitempty"`
	HeadSHA        string `json:"headSha,omitempty"`
	HeadRef        string `json:"headRef,omitempty"`
	BaseRef        string `json:"baseRef,omitempty"`
	BaseRepository string `json:"baseRepository,omitempty"`
	Mergeable      *bool  `json:"mergeable,omitempty"`
	MergeableState string `json:"mergeableState,omitempty"`
}

// Readiness is the complete answer of the gate. Ready is true only when Reasons
// is empty.
type Readiness struct {
	Ready        bool           `json:"ready"`
	HeadSHA      string         `json:"headSha,omitempty"`
	Pull         Pull           `json:"pull"`
	Verdict      domain.Verdict `json:"verdict"`
	VerdictFound bool           `json:"verdictFound"`
	Checks       []Check        `json:"checks"`
	Reasons      []string       `json:"reasons"`
}

// Outcome reports what GitHub actually did. It is never assumed: Merged is
// true only after reading the PR back, and Queued is reported verbatim.
type Outcome struct {
	Merged       bool   `json:"merged"`
	Queued       bool   `json:"queued"`
	QueueEntryID string `json:"queueEntryId,omitempty"`
	HeadSHA      string `json:"headSha,omitempty"`
	Method       string `json:"method,omitempty"`
}

// NotReadyError is returned by Merge when the gate refused the merge.
type NotReadyError struct {
	Readiness Readiness
}

func (e *NotReadyError) Error() string {
	if len(e.Readiness.Reasons) == 0 {
		return "pull request is not ready to merge"
	}
	return "pull request is not ready to merge: " + strings.Join(e.Readiness.Reasons, "; ")
}

// Options tunes the gate. The zero value is usable.
type Options struct {
	// Now is injectable for tests. Nil means time.Now.
	Now func() time.Time
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// ResolveVerify applies the documented defaults to a Hive verify config.
func ResolveVerify(v domain.VerifyConfig) domain.VerifyConfig {
	if v.Mode == "" {
		v.Mode = "on"
	}
	if v.MaxRounds <= 0 {
		v.MaxRounds = 3
	}
	if v.MergeMethod == "" {
		v.MergeMethod = "squash"
	}
	return v
}

// defaultMergeMethod reports the method to use, rejecting an invalid value so
// a typo can never silently fall back to a different merge strategy.
func defaultMergeMethod(v domain.VerifyConfig) (string, error) {
	method := ResolveVerify(v).MergeMethod
	switch method {
	case "merge", "squash", "rebase":
		return method, nil
	default:
		return "", fmt.Errorf("invalid merge method %q", method)
	}
}
