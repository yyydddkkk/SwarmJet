package domain

import "testing"

// A project may only reference repositories and a Meta URL on the GitLab
// authority it is configured with.
func TestProjectRejectsCrossHostAddresses(t *testing.T) {
	p := draftProject()
	p.GitLabURL = "https://gitlab.example"
	p.Repositories = []Repository{{ID: "svc", URL: "https://evil.example/team/svc.git", Path: "svc", TargetBranch: "main"}}
	if err := ValidateProjectForSave(p); err == nil {
		t.Fatal("cross-host repository URL accepted")
	}
	p.Repositories[0].URL = "git@gitlab.example:team/svc.git"
	if err := ValidateProjectForSave(p); err != nil {
		t.Fatalf("same-host SSH repository rejected: %v", err)
	}
	p.MetaURL = "https://evil.example/group/meta.git"
	if err := ValidateProjectForSave(p); err == nil {
		t.Fatal("cross-host Meta URL accepted")
	}
	p.MetaURL = "http://gitlab.example/group/meta.git"
	if err := ValidateProjectForSave(p); err != nil {
		t.Fatalf("same-host Meta URL rejected: %v", err)
	}
}
