package merge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

const (
	// Contract limits for the PR comment (and for anything echoed back).
	maxAttestationSummary  = 2000
	maxAttestationFinding  = 1000
	maxAttestationFindings = 20
)

// Actor identifies who is attesting. A user token uses its local address as
// ID; an agent token uses the Agent ID and its methodology role.
type Actor struct {
	ID       string
	Label    string
	RoleName string
	IsAgent  bool
}

// AttestRequest is one conclusion about a PR head.
type AttestRequest struct {
	Hive     domain.Hive
	PR       int64
	SHA      string
	Result   domain.AttestResult
	Summary  string
	Findings []domain.Finding
	Actor    Actor
}

// AttestOutcome reports what was stored and what happened to the PR comment.
type AttestOutcome struct {
	Attestation   domain.Attestation
	Created       bool
	Superseded    bool
	CommentPosted bool
	CommentError  string
	QueueError    string
}

// Attest validates and stores one conclusion, posts it as a PR comment with the
// App identity and asks for one merge check. A conclusion for a head that is no
// longer the PR head is stored as superseded and reported as such.
func (s *Service) Attest(ctx context.Context, req AttestRequest) (AttestOutcome, error) {
	var out AttestOutcome
	if req.PR <= 0 {
		return out, fmt.Errorf("invalid pull request number %d", req.PR)
	}
	if strings.TrimSpace(req.Hive.Repository) == "" {
		return out, errors.New("hive repository is empty")
	}
	if strings.TrimSpace(req.SHA) == "" {
		return out, errors.New("sha is required")
	}
	if strings.TrimSpace(req.Actor.ID) == "" {
		return out, errors.New("attester identity is required")
	}
	result := domain.AttestResult(strings.TrimSpace(string(req.Result)))
	switch result {
	case domain.AttestPass, domain.AttestPassNotes, domain.AttestFail, domain.AttestBlocked:
	default:
		return out, fmt.Errorf("result must be pass, pass_notes, fail or blocked, got %q", req.Result)
	}
	summary := strings.TrimSpace(req.Summary)
	if summary == "" {
		return out, errors.New("summary is required")
	}
	if s.store == nil || s.attestations == nil || s.github == nil {
		return out, errors.New("merge service is not configured")
	}
	if req.Actor.IsAgent {
		permissions, err := s.Permissions(ctx, req.Hive.ID, req.Actor.RoleName)
		if err != nil {
			return out, err
		}
		if !permissions.Attest {
			role := req.Actor.RoleName
			if role == "" {
				role = "default"
			}
			return out, fmt.Errorf("permission denied: role %q does not have the attest permission", role)
		}
	}

	client, repositoryID, err := s.github.GitHub(ctx, req.Hive.Repository)
	if err != nil {
		return out, fmt.Errorf("resolve GitHub client: %w", err)
	}
	pull, err := client.GetPullState(ctx, repositoryID, req.PR)
	if err != nil {
		return out, fmt.Errorf("read pull request: %w", err)
	}
	superseded := strings.TrimSpace(req.SHA) != pull.HeadSHA

	author, err := s.store.PRAuthorByBranch(ctx, req.Hive.ID, pull.HeadRef)
	if errors.Is(err, domain.ErrNotFound) {
		author, err = s.store.PRAuthor(ctx, req.Hive.ID, req.PR)
	}
	if errors.Is(err, domain.ErrNotFound) {
		author = domain.Agent{}
	} else if err != nil {
		return out, fmt.Errorf("read pull request author: %w", err)
	}
	isAuthor := author.ID != "" && author.ID == req.Actor.ID

	attestation := domain.Attestation{
		HiveID:     req.Hive.ID,
		PRNumber:   req.PR,
		HeadSHA:    strings.TrimSpace(req.SHA),
		AttesterID: req.Actor.ID,
		AuthorID:   author.ID,
		IsAuthor:   isAuthor,
		Result:     result,
		Summary:    truncateRunes(summary, maxAttestationSummary),
		Findings:   clampFindings(req.Findings),
		Superseded: superseded,
		CreatedAt:  s.now().UTC(),
	}
	stored, created, err := s.attestations.RecordAttestation(ctx, attestation)
	if err != nil {
		return out, fmt.Errorf("record attestation: %w", err)
	}
	out = AttestOutcome{Attestation: stored, Created: created, Superseded: stored.Superseded}

	if created || stored.CommentURL == "" {
		body := renderAttestationComment(stored, req.Actor.Label)
		commentURL, commentErr := client.CreatePRComment(ctx, repositoryID, req.PR, body)
		if commentErr != nil {
			out.CommentError = commentErr.Error()
		} else {
			out.CommentPosted = true
			out.Attestation.CommentURL = commentURL
			if err = s.attestations.SetAttestationComment(ctx, stored.ID, commentURL); err != nil {
				out.CommentError = "comment was posted but its URL could not be stored: " + err.Error()
			}
		}
	}
	if err = s.enqueue(ctx, req.Hive.ID, req.PR, req.SHA); err != nil {
		out.QueueError = err.Error()
	}
	return out, nil
}

// clampFindings applies the comment limits before storage: at most 20 findings,
// each field at most 1000 characters.
func clampFindings(findings []domain.Finding) []domain.Finding {
	out := make([]domain.Finding, 0, len(findings))
	for index, finding := range findings {
		if index >= maxAttestationFindings {
			break
		}
		out = append(out, domain.Finding{
			Severity: strings.TrimSpace(finding.Severity),
			Location: truncateRunes(strings.TrimSpace(finding.Location), maxAttestationFinding),
			Finding:  truncateRunes(strings.TrimSpace(finding.Finding), maxAttestationFinding),
			Evidence: truncateRunes(strings.TrimSpace(finding.Evidence), maxAttestationFinding),
		})
	}
	return out
}

func renderAttestationComment(a domain.Attestation, label string) string {
	if strings.TrimSpace(label) == "" {
		label = a.AttesterID
	}
	var b strings.Builder
	b.WriteString("## 结论：")
	b.WriteString(strings.ToUpper(string(a.Result)))
	b.WriteString("\n\n")
	if a.Superseded {
		b.WriteString("> 该结论对应的提交已不是当前 head，已记为作废。\n\n")
	}
	fmt.Fprintf(&b, "- 提交：`%s`\n", a.HeadSHA)
	fmt.Fprintf(&b, "- 给出者：%s\n", label)
	fmt.Fprintf(&b, "- 是否作者：%s\n", authorLabel(a))
	b.WriteString("\n摘要：\n\n")
	if a.Summary == "" {
		b.WriteString("（无）")
	} else {
		b.WriteString(a.Summary)
	}
	if len(a.Findings) > 0 {
		b.WriteString("\n\n发现：\n\n")
		for index, finding := range a.Findings {
			b.WriteString(formatFinding(index+1, finding))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func formatFinding(index int, finding domain.Finding) string {
	severity := strings.TrimSpace(finding.Severity)
	if severity == "" {
		severity = "note"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d. [%s] ", index, severity)
	if finding.Location != "" {
		b.WriteString(finding.Location)
		b.WriteString(": ")
	}
	b.WriteString(finding.Finding)
	if finding.Evidence != "" {
		b.WriteString("\n   evidence: ")
		b.WriteString(finding.Evidence)
	}
	return truncateRunes(b.String(), maxAttestationFinding)
}

func authorLabel(a domain.Attestation) string {
	switch {
	case a.AuthorID == "":
		return "未知"
	case a.IsAuthor:
		return "是"
	default:
		return "否"
	}
}

// truncateRunes bounds s to max runes including a trailing ellipsis.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}
