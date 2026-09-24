package domain

import "testing"

func TestDigestBindsEveryRepositoryAndTarget(t *testing.T) {
	a := []Revision{{RepositoryID: "a", BaseSHA: "base", HeadSHA: "1", TargetBranch: "dev"}, {RepositoryID: "b", HeadSHA: "2", TargetBranch: "main"}}
	b := []Revision{a[1], a[0]}
	if Digest(a) != Digest(b) {
		t.Fatal("order affected digest")
	}
	b[0].HeadSHA = "3"
	if Digest(a) == Digest(b) {
		t.Fatal("new head retained approval")
	}
}
func TestMergeOrderAndCycles(t *testing.T) {
	r := []Repository{{ID: "client", DependsOn: []string{"server"}}, {ID: "server"}}
	o := MergeOrder(r)
	if o[0] != "server" {
		t.Fatal(o)
	}
	r[1].DependsOn = []string{"client"}
	o = MergeOrder(r)
	if len(o) != 2 || o[0] != "client" {
		t.Fatal(o)
	}
}
func TestValidBranch(t *testing.T) {
	for _, s := range []string{"dev", "feature/example", "release-1"} {
		if !ValidBranch(s) {
			t.Fatal(s)
		}
	}
	for _, s := range []string{"", "-x", "a..b", "a.lock", "x y", "x@{1}"} {
		if ValidBranch(s) {
			t.Fatal(s)
		}
	}
}

func TestEnabledProjectCanDisableExtraAcceptance(t *testing.T) {
	p := Project{
		ID: "project", Name: "Project", GitLabURL: "https://gitlab.example", MetaURL: "https://gitlab.example/team/meta.git", MetaRef: "main",
		CredentialRef: "GITLAB_TOKEN", AcceptanceStrategy: "none",
		Repositories: []Repository{{ID: "service", ProjectID: 1, URL: "https://gitlab.example/team/service.git", Path: "service", TargetBranch: "main"}},
		Development:  AgentConfig{Adapter: "dst", Image: "agent:test", TimeoutSeconds: 300},
		Review:       AgentConfig{Adapter: "dst", Image: "agent:test", TimeoutSeconds: 300},
	}
	if err := ValidateProject(p); err != nil {
		t.Fatal("optional acceptance rejected:", err)
	}
	p.AcceptanceStrategy = ""
	if err := ValidateProject(p); err == nil {
		t.Fatal("legacy custom acceptance unexpectedly became disabled")
	}
}

func TestDisabledIndependentReviewDoesNotRequireReviewAgent(t *testing.T) {
	p := Project{
		ID: "project", Name: "Project", GitLabURL: "https://gitlab.example", MetaURL: "https://gitlab.example/team/meta.git", MetaRef: "main",
		CredentialRef: "GITLAB_TOKEN", AcceptanceStrategy: "none", ReviewMode: "none",
		Repositories: []Repository{{ID: "service", ProjectID: 1, URL: "https://gitlab.example/team/service.git", Path: "service", TargetBranch: "main"}},
		Development:  AgentConfig{Adapter: "dst", Image: "agent:test", TimeoutSeconds: 300},
	}
	if err := ValidateProject(p); err != nil {
		t.Fatal(err)
	}
	p.ReviewMode = "independent"
	if err := ValidateProject(p); err == nil {
		t.Fatal("enabled independent review accepted without a review Agent")
	}
}

func draftProject() Project {
	return Project{ID: "draft", Name: "Draft", MetaURL: "https://gitlab.example/group/meta.git"}
}
func TestDraftProjectCanBeIncompleteButExecutionValidationStaysStrict(t *testing.T) {
	p := draftProject()
	if err := ValidateProjectForSave(p); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProject(p); err == nil {
		t.Fatal("incomplete draft accepted for execution")
	}
	p.Repositories = []Repository{{}, {ID: "service"}}
	p.Development = AgentConfig{Adapter: "opencode"}
	p.Integration.Checks = []Check{{}, {Name: "tests"}}
	if err := ValidateProjectForSave(p); err != nil {
		t.Fatal("partial draft rejected", err)
	}
	p.Enabled = true
	if err := ValidateProjectForSave(p); err == nil {
		t.Fatal("incomplete project activated")
	}
}
func TestDraftRejectsUnsafeSuppliedValues(t *testing.T) {
	cases := map[string]func(*Project){
		"missing meta":         func(p *Project) { p.MetaURL = "" },
		"local meta":           func(p *Project) { p.MetaURL = "file:///tmp/meta" },
		"url password":         func(p *Project) { p.MetaURL = "https://user:token@gitlab.example/meta.git" },
		"url query":            func(p *Project) { p.MetaURL += "?token=private" },
		"url fragment":         func(p *Project) { p.MetaURL += "#" },
		"gitlab query":         func(p *Project) { p.GitLabURL = "https://gitlab.example?token=private" },
		"env reference":        func(p *Project) { p.CredentialRef = "token-value" },
		"meta ref":             func(p *Project) { p.MetaRef = "-option" },
		"model":                func(p *Project) { p.Development.Model = "model-without-provider" },
		"unsupported adapter":  func(p *Project) { p.Review.Adapter = "unknown" },
		"timeout":              func(p *Project) { p.Review.TimeoutSeconds = -1 },
		"image":                func(p *Project) { p.Review.Image = "--privileged" },
		"repo path":            func(p *Project) { p.Repositories = []Repository{{Path: "../escape"}} },
		"absolute repo path":   func(p *Project) { p.Repositories = []Repository{{Path: "/tmp"}} },
		"git metadata":         func(p *Project) { p.Repositories = []Repository{{Path: "service/.git"}} },
		"windows path":         func(p *Project) { p.Repositories = []Repository{{Path: `..\escape`}} },
		"negative project id":  func(p *Project) { p.Repositories = []Repository{{ProjectID: -1}} },
		"duplicate id":         func(p *Project) { p.Repositories = []Repository{{ID: "same"}, {ID: "same"}} },
		"duplicate project id": func(p *Project) { p.Repositories = []Repository{{ProjectID: 1}, {ProjectID: 1}} },
		"duplicate path":       func(p *Project) { p.Repositories = []Repository{{Path: "service"}, {Path: "service"}} },
		"branch":               func(p *Project) { p.Repositories = []Repository{{TargetBranch: "bad..ref"}} },
		"dependency":           func(p *Project) { p.Repositories = []Repository{{DependsOn: []string{"../bad"}}} },
		"compose path":         func(p *Project) { p.Integration.ComposeFile = "../compose.yml" },
		"argv":                 func(p *Project) { p.Integration.Checks = []Check{{Command: []string{""}}} },
		"argv nul":             func(p *Project) { p.Integration.Checks = []Check{{Command: []string{"printf", "x\x00y"}}} },
		"service":              func(p *Project) { p.Integration.Checks = []Check{{Service: "../escape"}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := draftProject()
			mutate(&p)
			if err := ValidateProjectForSave(p); err == nil {
				t.Fatal("unsafe supplied draft value accepted")
			}
		})
	}
}
func TestDraftSupportsSSHAndSensitivePatterns(t *testing.T) {
	for _, remote := range []string{"git@gitlab.example:group/meta.git", "ssh://git@gitlab.example/group/meta.git", "https://gitlab.example/group/meta.git"} {
		p := draftProject()
		p.MetaURL = remote
		p.Repositories = []Repository{{SensitivePaths: []string{".github/workflows/**", "db/migrations/**"}}}
		if err := ValidateProjectForSave(p); err != nil {
			t.Fatal(remote, err)
		}
	}
}
