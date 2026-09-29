package service

import (
	"context"
	"log/slog"
	"slices"
	"swarmjet/internal/domain"
	"swarmjet/internal/forge"
)

const deniedTriggerMessage = "无法接单：触发者未通过项目权限验证。请由已绑定 GitLab 身份的项目成员重新移除并添加 autonomous 标签，或发表新的 /swarmjet run 评论。"

// latestAutonomousAdd rejects missing, inconsistent, or incomplete history.
// GitLab does not promise event ordering, so compare timestamps and IDs.
func latestAutonomousAdd(events []forge.LabelEvent) (forge.LabelEvent, bool) {
	var latest forge.LabelEvent
	for _, event := range events {
		if event.Label.Name != "autonomous" || event.ID <= 0 {
			continue
		}
		if latest.ID == 0 || event.CreatedAt.After(latest.CreatedAt) || event.CreatedAt.Equal(latest.CreatedAt) && event.ID > latest.ID {
			latest = event
		}
	}
	return latest, latest.ID > 0 && latest.Action == "add" && latest.User.ID > 0
}

func (s *Service) authorizeInitialTrigger(ctx context.Context, g forge.Client, p domain.Project, issue domain.Issue) (bool, error) {
	if p.HostingProvider() == domain.ProviderGitHub {
		if !p.GitHubAutoIntake {
			return false, nil
		}
		if err := s.auditTrigger(ctx, p, issue, "label", issue.IID, 0, "accepted", "legacy project switch"); err != nil {
			return false, err
		}
		return true, nil
	}
	if !slices.Contains(issue.Labels, "autonomous") {
		return false, nil
	}
	if p.GitLabTriggerMode == "all" {
		if err := s.auditTrigger(ctx, p, issue, "label", 0, 0, "accepted", "all labeled issues"); err != nil {
			return false, err
		}
		return true, nil
	}
	reader, ok := g.(forge.LabelEventReader)
	if !ok {
		if err := s.auditTrigger(ctx, p, issue, "label", 0, 0, "denied", "label event reader unavailable"); err != nil {
			return false, err
		}
		return s.rejectTrigger(ctx, g, issue, "GitLab 连接无法核验标签事件"), nil
	}
	events, err := reader.ListIssueLabelEvents(ctx, issue)
	if err != nil {
		if err := s.auditTrigger(ctx, p, issue, "label", 0, 0, "denied", "label event lookup failed"); err != nil {
			return false, err
		}
		return s.rejectTrigger(ctx, g, issue, "GitLab 标签事件暂不可用"), nil
	}
	event, valid := latestAutonomousAdd(events)
	if !valid {
		if err := s.auditTrigger(ctx, p, issue, "label", event.ID, event.User.ID, "denied", "label actor unavailable"); err != nil {
			return false, err
		}
		return s.rejectTrigger(ctx, g, issue, "无法确认 autonomous 标签的添加者"), nil
	}
	denied, err := s.Store.DeniedIssueLabelEvent(ctx, p.HostingURL(), issue, event.ID)
	if err != nil {
		return false, err
	}
	if denied {
		if err := s.auditTrigger(ctx, p, issue, "label", event.ID, event.User.ID, "denied", "previously denied"); err != nil {
			return false, err
		}
		return s.rejectTrigger(ctx, g, issue, "此标签事件此前已被拒绝"), nil
	}
	allowed := false
	if p.GitLabTriggerMode == "owner" || p.GitLabTriggerMode == "operator" {
		allowed, err = s.Store.AuthorizedGitLabActor(ctx, p.ID, p.GitLabURL, event.User.ID, p.GitLabTriggerMode == "operator")
		if err != nil {
			return false, err
		}
	}
	if !allowed {
		reason := "触发者不是当前允许接单的项目成员"
		if p.GitLabTriggerMode == "" || p.GitLabTriggerMode == "off" {
			reason = "此项目已关闭 GitLab 自动接单"
		}
		if err := s.Store.RecordDeniedIssueLabelEvent(ctx, p.HostingURL(), issue, event.ID, reason); err != nil {
			return false, err
		}
		if err := s.auditTrigger(ctx, p, issue, "label", event.ID, event.User.ID, "denied", reason); err != nil {
			return false, err
		}
		return s.rejectTrigger(ctx, g, issue, reason), nil
	}
	if err := s.auditTrigger(ctx, p, issue, "label", event.ID, event.User.ID, "accepted", ""); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) auditTrigger(ctx context.Context, p domain.Project, issue domain.Issue, kind string, eventID, externalID int64, decision, reason string) error {
	if s.Store == nil {
		return nil
	} // Pure policy tests do not attach persistence.
	return s.Store.RecordExternalTriggerAudit(ctx, p.ID, string(p.HostingProvider()), domain.IssueSource(p.HostingURL(), issue), kind, eventID, externalID, decision, reason)
}

func (s *Service) rejectTrigger(ctx context.Context, g forge.Client, issue domain.Issue, reason string) bool {
	if err := g.UpsertProgress(ctx, issue, "dispatch", deniedTriggerMessage+" 原因："+reason); err != nil {
		// Feedback errors must not turn denial into admission.
		slog.Warn("trigger rejection feedback failed", "error", err)
	}
	return false
}

func (s *Service) authorizeRunNote(ctx context.Context, g forge.Client, p domain.Project, issue domain.Issue, note forge.IssueNote) (bool, error) {
	if p.HostingProvider() == domain.ProviderGitHub {
		if !p.GitHubAutoIntake {
			return false, nil
		}
		if err := s.auditTrigger(ctx, p, issue, "comment", note.ID, note.Author.ID, "accepted", "legacy project switch"); err != nil {
			return false, err
		}
		return true, nil
	}
	denied, err := s.Store.DeniedIssueRunCommand(ctx, p.HostingURL(), issue, note.ID)
	if err != nil {
		return false, err
	}
	if denied {
		if err := s.auditTrigger(ctx, p, issue, "comment", note.ID, note.Author.ID, "denied", "previously denied"); err != nil {
			return false, err
		}
		return s.rejectTrigger(ctx, g, issue, "此评论此前已被拒绝"), nil
	}
	allowed := false
	if note.Author.ID > 0 && (p.GitLabTriggerMode == "owner" || p.GitLabTriggerMode == "operator") {
		allowed, err = s.Store.AuthorizedGitLabActor(ctx, p.ID, p.GitLabURL, note.Author.ID, p.GitLabTriggerMode == "operator")
		if err != nil {
			return false, err
		}
	}
	if !allowed {
		if err := s.Store.RecordDeniedIssueRunCommand(ctx, p.HostingURL(), issue, note.ID, "comment author is not authorized"); err != nil {
			return false, err
		}
		if err := s.auditTrigger(ctx, p, issue, "comment", note.ID, note.Author.ID, "denied", "comment author is not authorized"); err != nil {
			return false, err
		}
		return s.rejectTrigger(ctx, g, issue, "评论作者未通过项目权限验证"), nil
	}
	if err := s.auditTrigger(ctx, p, issue, "comment", note.ID, note.Author.ID, "accepted", ""); err != nil {
		return false, err
	}
	return true, nil
}
