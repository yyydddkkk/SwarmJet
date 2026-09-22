package initflow

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestVerificationOverridesImageEntrypoint(t *testing.T) {
	f := newFixture(t)
	f.m.VerifyImage = "agent:test"
	var got []string
	f.m.Docker = func(_ context.Context, args []string) ([]byte, error) {
		if args[0] == "run" {
			got = append([]string{}, args...)
		}
		return nil, nil
	}
	argv := []string{"node", "-e", "console.log('two words')"}
	if _, _, err := f.m.dockerRun(context.Background(), "p1", 0, t.TempDir(), argv); err != nil {
		t.Fatal(err)
	}
	for i, arg := range got {
		if arg == "--entrypoint" {
			want := []string{"--entrypoint", "node", "agent:test", "-e", "console.log('two words')"}
			if !reflect.DeepEqual(got[i:], want) {
				t.Fatalf("unexpected executable arguments: %v", got[i:])
			}
			return
		}
	}
	t.Fatal("image entrypoint was not overridden")
}

func TestVerificationEntrypointWithDocker(t *testing.T) {
	image := os.Getenv("SWARMJET_TEST_VERIFY_IMAGE")
	if image == "" {
		t.Skip("set SWARMJET_TEST_VERIFY_IMAGE for real Docker verification")
	}
	m := &Manager{DataDir: t.TempDir(), VerifyImage: image}
	code, out, err := m.dockerRun(context.Background(), "entrypoint-test", 0, t.TempDir(), []string{"node", "-e", "console.log(process.argv[1]); process.exit(7)", "two words"})
	if err != nil || code != 7 || strings.TrimSpace(out) != "two words" {
		t.Fatalf("verification did not execute node: code=%d output=%q err=%v", code, out, err)
	}
}
