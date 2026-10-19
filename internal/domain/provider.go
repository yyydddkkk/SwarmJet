package domain

// Provider identifies a hosting platform. Empty preserves legacy GitLab
// snapshots written before GitHub support existed.
type Provider string

const (
	ProviderGitLab Provider = "gitlab"
	ProviderGitHub Provider = "github"
)

// Effective resolves the legacy empty provider to GitLab.
func (p Provider) Effective() Provider {
	if p == "" {
		return ProviderGitLab
	}
	return p
}
