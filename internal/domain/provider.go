package domain

import "fmt"

// Provider identifies a hosting platform. Empty preserves legacy GitLab snapshots.
type Provider string

const (
	ProviderGitLab Provider = "gitlab"
	ProviderGitHub Provider = "github"
)

func (p Provider) Effective() Provider {
	if p == "" {
		return ProviderGitLab
	}
	return p
}
func (p Provider) Validate() error {
	if p.Effective() != ProviderGitLab && p.Effective() != ProviderGitHub {
		return fmt.Errorf("unsupported hosting provider %q", p)
	}
	return nil
}
func (p Project) HostingProvider() Provider    { return p.Provider.Effective() }
func (c Connection) HostingProvider() Provider { return c.Provider.Effective() }
func (i Issue) HostingProvider() Provider      { return i.Provider.Effective() }
func (i IssueRef) HostingProvider() Provider   { return i.Provider.Effective() }
func (p Project) HostingURL() string {
	if p.HostingProvider() == ProviderGitHub {
		return "https://github.com"
	}
	return p.GitLabURL
}
func (p Project) EffectiveMergeMethod() string {
	if p.MergeMethod == "" {
		return "merge"
	}
	return p.MergeMethod
}

// IssueSource preserves the historical GitLab key exactly. GitHub's numeric
// repository ID remains stable across renames and is scoped by platform.
func IssueSource(base string, issue Issue) string {
	if issue.HostingProvider() == ProviderGitHub {
		return fmt.Sprintf("github/%d/%d", issue.ProjectID, issue.IID)
	}
	return fmt.Sprintf("%s/%d/%d", GitLabIdentity(base), issue.ProjectID, issue.IID)
}

func validateProviderProject(p Project) error {
	if err := p.Provider.Validate(); err != nil {
		return err
	}
	switch p.MergeMethod {
	case "", "merge", "squash", "rebase":
	default:
		return fmt.Errorf("unknown merge method")
	}
	if p.HostingProvider() != ProviderGitHub {
		return nil
	}
	if p.AcceptanceStrategy == "gitlab_ci" {
		return fmt.Errorf("GitHub projects must use platform_ci for hosting checks")
	}
	if !p.ManagedMeta() {
		return fmt.Errorf("GitHub projects require managed Meta-Repo")
	}
	if p.ConnectionID == "" {
		return fmt.Errorf("GitHub projects require an App installation connection")
	}
	if p.CredentialRef != "" || p.MergeCredentialRef != "" {
		return fmt.Errorf("GitHub projects use App installation credentials")
	}
	if p.GitLabURL != "" {
		return fmt.Errorf("GitHub projects must not set gitlabUrl")
	}
	for _, r := range p.Repositories {
		if r.URL != "" && (!safeRepositoryURL(r.URL) || !sameGitLabAuthority("https://github.com", r.URL)) {
			return fmt.Errorf("GitHub repository URL must use github.com")
		}
	}
	return nil
}
