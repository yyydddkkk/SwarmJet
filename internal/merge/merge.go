// Package merge decides whether a pull request may merge and performs the
// merge through the platform's GitHub App identity.
//
// The rules come from the Hive's methodology. Nothing here hard-codes a
// workflow: the platform evaluates the declared MergePolicy faithfully and
// never bypasses GitHub's own branch protection or approval requirements.
package merge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/github"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
)

// Store is the subset of the Hive store the merge service and scheduler need.
type Store interface {
	Hive(ctx context.Context, id string) (domain.Hive, error)
	Agent(ctx context.Context, id string) (domain.Agent, error)
	PRAuthor(ctx context.Context, hiveID string, pr int64) (domain.Agent, error)
	PRAuthorByBranch(ctx context.Context, hiveID, branch string) (domain.Agent, error)
	Inbox(ctx context.Context, hiveID string, to domain.Address, afterID string, limit int) ([]domain.Message, error)
	SendMessage(ctx context.Context, m domain.Message) (domain.Message, error)
}

// AttestationStore is the attestation persistence contract. *store.Store
// implements it.
type AttestationStore interface {
	RecordAttestation(ctx context.Context, a domain.Attestation) (domain.Attestation, bool, error)
	SetAttestationComment(ctx context.Context, id, url string) error
	LatestAttestation(ctx context.Context, hiveID string, pr int64) (domain.Attestation, error)
	Attestations(ctx context.Context, hiveID string, pr int64) ([]domain.Attestation, error)
	SupersedeAttestations(ctx context.Context, hiveID string, pr int64, keepSHA string) error
}

// MethodologySource reads the Hive's active methodology. The methodology task
// implements it; when the store has no methodology the service falls back to
// the built-in minimum.
type MethodologySource interface {
	ActiveMethodology(ctx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error)
}

// GitHub is the read/comment/merge surface the service needs. *github.Client
// implements it.
type GitHub interface {
	GetPullState(ctx context.Context, id, number int64) (github.PullState, error)
	RequiredChecks(ctx context.Context, id, number int64, sha string) (github.CheckResult, error)
	CheckRuns(ctx context.Context, id int64, sha string) ([]github.CheckRun, error)
	RequestMerge(ctx context.Context, id, number int64, sha, method string) (github.MergeResult, error)
	GetMergeState(ctx context.Context, id, number int64, expectedSHA string) (github.MergeResult, error)
	CreatePRComment(ctx context.Context, id, number int64, body string) (string, error)
}

// GitHubSource returns a client scoped to one Hive repository plus its numeric
// GitHub ID. The production source mints a repository-scoped installation
// token; tests use StaticSource.
type GitHubSource interface {
	GitHub(ctx context.Context, repository string) (GitHub, int64, error)
}

// StaticSource always returns the same client and repository ID. It is useful
// for tests and for callers that already resolved the repository token.
type StaticSource struct {
	Client       GitHub
	RepositoryID int64
}

func (s StaticSource) GitHub(context.Context, string) (GitHub, int64, error) {
	if s.Client == nil {
		return nil, 0, errors.New("GitHub client is not configured")
	}
	id := s.RepositoryID
	if id <= 0 {
		id = 1
	}
	return s.Client, id, nil
}

// Inserter enqueues the merge_check River job. *jobs.Client implements it.
type Inserter interface {
	InsertMergeCheck(ctx context.Context, job jobs.MergeCheck) error
}

// Options configures the service. Store, Attestations and GitHub are required.
type Options struct {
	Store        Store
	Attestations AttestationStore
	Methodology  MethodologySource
	GitHub       GitHubSource
	Inserter     Inserter
	// Now is injectable for tests. Nil means time.Now.
	Now func() time.Time
}

// Service evaluates merge conditions, records attestations and decides whether
// an automatic merge may run.
type Service struct {
	store        Store
	attestations AttestationStore
	methodology  MethodologySource
	github       GitHubSource
	inserter     Inserter
	now          func() time.Time
}

func New(opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:        opts.Store,
		attestations: opts.Attestations,
		methodology:  opts.Methodology,
		github:       opts.GitHub,
		inserter:     opts.Inserter,
		now:          now,
	}
}

// SetInserter wires the queue after it is created. It exists because the River
// client also needs the scheduler's handler, so wiring is necessarily two-step.
func (s *Service) SetInserter(inserter Inserter) { s.inserter = inserter }

// BuiltinPolicy is the minimum methodology's merge policy: CI must pass, no
// attestation is required, and merging is never automatic.
func BuiltinPolicy() domain.MergePolicy {
	return resolvePolicy(domain.MergePolicy{RequireAttestation: false, RequireChecks: true, Auto: false})
}

func resolvePolicy(p domain.MergePolicy) domain.MergePolicy {
	if p.Method == "" {
		p.Method = "squash"
	}
	if len(p.AcceptResults) == 0 {
		p.AcceptResults = []domain.AttestResult{domain.AttestPass, domain.AttestPassNotes}
	}
	return p
}

// Policy returns the active merge policy, falling back to the built-in minimum
// when the Hive has no methodology yet.
func (s *Service) Policy(ctx context.Context, hiveID string) (domain.MergePolicy, error) {
	if s.methodology == nil {
		return BuiltinPolicy(), nil
	}
	m, _, err := s.methodology.ActiveMethodology(ctx, hiveID)
	if errors.Is(err, domain.ErrNotFound) {
		return BuiltinPolicy(), nil
	}
	if err != nil {
		return domain.MergePolicy{}, fmt.Errorf("read active methodology: %w", err)
	}
	return resolvePolicy(m.Manifest.Merge), nil
}

// Permissions resolves a role's platform-enforced permissions. An empty role
// name means the methodology's default role; without a methodology the built-in
// worker role is used.
func (s *Service) Permissions(ctx context.Context, hiveID, roleName string) (domain.RolePermissions, error) {
	if s.methodology == nil {
		return builtinRole(roleName)
	}
	m, _, err := s.methodology.ActiveMethodology(ctx, hiveID)
	if errors.Is(err, domain.ErrNotFound) {
		return builtinRole(roleName)
	}
	if err != nil {
		return domain.RolePermissions{}, fmt.Errorf("read active methodology: %w", err)
	}
	if roleName == "" {
		roleName = m.Manifest.DefaultRole
	}
	if roleName == "" {
		roleName = "worker"
	}
	role, ok := m.Manifest.Roles[roleName]
	if !ok {
		return domain.RolePermissions{}, fmt.Errorf("methodology has no role %q", roleName)
	}
	return role.Permissions, nil
}

func builtinRole(roleName string) (domain.RolePermissions, error) {
	if roleName == "" || roleName == "worker" {
		return domain.RolePermissions{Publish: true, Message: true}, nil
	}
	return domain.RolePermissions{}, fmt.Errorf("methodology has no role %q", roleName)
}

// Attestations lists a PR's conclusion history, superseded rows included.
func (s *Service) Attestations(ctx context.Context, hiveID string, pr int64) ([]domain.Attestation, error) {
	if s.attestations == nil {
		return nil, errors.New("attestation storage is not configured")
	}
	return s.attestations.Attestations(ctx, hiveID, pr)
}

// Check is one GitHub check run in a readiness or status result.
type Check struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url,omitempty"`
	Output     string `json:"output,omitempty"`
}

// Pull is the live PR state used by the gate.
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
	Ready            bool               `json:"ready"`
	HeadSHA          string             `json:"headSha,omitempty"`
	Pull             Pull               `json:"pull"`
	Attestation      domain.Attestation `json:"attestation"`
	AttestationFound bool               `json:"attestationFound"`
	Checks           []Check            `json:"checks"`
	Reasons          []string           `json:"reasons"`
}

// Outcome reports what GitHub actually did. Merged is true only after reading
// the PR back; Queued means a merge queue entry exists.
type Outcome struct {
	Merged       bool   `json:"merged"`
	Queued       bool   `json:"queued"`
	QueueEntryID string `json:"queueEntryId,omitempty"`
	HeadSHA      string `json:"headSha,omitempty"`
	Method       string `json:"method,omitempty"`
}

// NotReadyError is returned by Merge when the declared conditions do not hold.
type NotReadyError struct {
	Readiness Readiness
}

func (e *NotReadyError) Error() string {
	if len(e.Readiness.Reasons) == 0 {
		return "pull request is not ready to merge"
	}
	return "pull request is not ready to merge: " + strings.Join(e.Readiness.Reasons, "; ")
}

func (s *Service) enqueue(ctx context.Context, hiveID string, pr int64, sha string) error {
	if s.inserter == nil || hiveID == "" || pr <= 0 || strings.TrimSpace(sha) == "" {
		return nil
	}
	return s.inserter.InsertMergeCheck(ctx, jobs.MergeCheck{HiveID: hiveID, PR: pr, SHA: sha})
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func hasConflict(pull github.PullState) bool {
	if pull.Mergeable != nil && !*pull.Mergeable {
		return true
	}
	switch strings.ToLower(pull.MergeableState) {
	case "dirty", "conflicting":
		return true
	default:
		return false
	}
}

func policyAccepts(p domain.MergePolicy, result domain.AttestResult) bool {
	for _, accepted := range p.AcceptResults {
		if accepted == result {
			return true
		}
	}
	return false
}

// failedConclusion reports a check conclusion that must block a merge or wake
// the PR owner. Neutral, skipped and successful runs are not failures.
func failedConclusion(conclusion string) bool {
	switch strings.ToLower(strings.TrimSpace(conclusion)) {
	case "failure", "timed_out", "action_required", "startup_failure", "cancelled":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
