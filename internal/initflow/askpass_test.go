package initflow

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"swarmjet/internal/domain"
)

func TestInitializationUsesNativeAskpass(t *testing.T) {
	helper, err := filepath.Abs("../../scripts/askpass.sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMJET_GIT_ASKPASS", helper)
	m := &Manager{DataDir: t.TempDir(), Credential: func(string) (string, error) { return "test-token", nil }}
	environment := m.gitEnv(domain.Project{ID: "p1", CredentialRef: "TEST_TOKEN"})
	// Run Git's authentication prompt path without contacting a real repository.
	cmd := exec.Command("git", "-c", "credential.helper=", "credential", "fill")
	cmd.Env = environment
	input, err := os.CreateTemp(t.TempDir(), "credential-input")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err = input.WriteString("protocol=https\nhost=gitlab.example\n\n"); err != nil {
		t.Fatal(err)
	}
	if _, err = input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = input
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Git could not authenticate through native helper: %v", err)
	}
	if !strings.Contains(string(output), "username=oauth2") || !strings.Contains(string(output), "password=test-token") {
		t.Fatal("helper did not supply expected test credentials")
	}
}
