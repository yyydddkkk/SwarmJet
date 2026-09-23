package imagebuild

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"swarmjet/internal/domain"
)

const digestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const digestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func awaitBuild(t *testing.T, m *Manager, id string) Build {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, b := range m.Snapshot().Builds {
			if b.ID == id && b.Status != "building" && b.Status != "cancelling" {
				return b
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("build never finished")
	return Build{}
}

func TestBuildSnapshotLogsAndExplicitActivation(t *testing.T) {
	dir := t.TempDir()
	started := make(chan []string, 1)
	release := make(chan struct{})
	m, err := New(dir, func(ctx context.Context, args []string, out io.Writer) error {
		if args[0] == "build" {
			started <- args
			_, _ = io.WriteString(out, "installing selected CLIs\n")
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		_, _ = io.WriteString(out, digestA+"\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	if len(c.Agents) != 0 {
		t.Fatal("default selects agents")
	}
	c.Agents = []string{"codex"}
	c.ExtraPackages = []string{"ffmpeg"}
	b, err := m.Build(c)
	if err != nil {
		t.Fatal(err)
	}
	args := <-started
	if !strings.Contains(strings.Join(args, " "), "AGENT_CLIS=codex") || !strings.Contains(strings.Join(args, " "), "EXTRA_APT_PACKAGES=ffmpeg") {
		t.Fatal(args)
	}
	buildContext := args[len(args)-1]
	if !strings.HasPrefix(buildContext, dir) {
		t.Fatal("unexpected build context", buildContext)
	}
	files := []string{}
	if err = filepath.WalkDir(buildContext, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(buildContext, path)
			files = append(files, rel)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files, []string{"deploy/Dockerfile.agent", "scripts/askpass.sh", "scripts/install-agent-clis.sh"}) {
		t.Fatal("unreviewed build context", files)
	}
	if _, err = m.Build(c); err == nil {
		t.Fatal("concurrent build accepted")
	}
	if _, err = m.Activate(context.Background(), b.ID); err == nil {
		t.Fatal("unfinished build activated")
	}
	if active, err := ReadActive(dir); err != nil || active != nil {
		t.Fatal("build implicitly switched image", active, err)
	}
	c.Agents = []string{"claude"}
	if err = m.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	close(release)
	finished := awaitBuild(t, m, b.ID)
	if finished.Status != "succeeded" || finished.ImageID != digestA || !reflect.DeepEqual(finished.Config.Agents, []string{"codex"}) {
		t.Fatal(finished)
	}
	log, err := m.Logs(b.ID)
	if err != nil || !strings.Contains(log, "installing selected") {
		t.Fatal(log, err)
	}
	active, err := m.Activate(context.Background(), b.ID)
	if err != nil || active.ImageID != digestA {
		t.Fatal(active, err)
	}
	p := domain.Project{Development: domain.AgentConfig{Image: "old:tag"}, Review: domain.AgentConfig{Image: "old:review"}}
	accepted, err := ApplyToProject(dir, p)
	if err != nil || accepted.Development.Image != digestA || accepted.Review.Image != digestA || p.Development.Image != "old:tag" {
		t.Fatal(accepted, err)
	}
	// A restart retains recipes/history/active digest without starting another build.
	restarted, err := New(dir, func(context.Context, []string, io.Writer) error {
		t.Error("restart launched docker")
		return errors.New("unexpected")
	})
	if err != nil || restarted.Snapshot().Config.Agents[0] != "claude" {
		t.Fatal(err)
	}
	if image, err := CurrentImage(dir, "fallback"); err != nil || image != digestA {
		t.Fatal(image, err)
	}
}

func TestFailedCancelledAndInterruptedBuildsKeepActiveImage(t *testing.T) {
	for _, mode := range []string{"failed", "cancelled", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if err := writeJSON(filepath.Join(root(dir), "active.json"), Active{ImageID: digestA}); err != nil {
				t.Fatal(err)
			}
			m, err := New(dir, func(ctx context.Context, args []string, out io.Writer) error {
				if mode == "cancelled" {
					<-ctx.Done()
					return ctx.Err()
				}
				return errors.New("simulated build failure")
			})
			if err != nil {
				t.Fatal(err)
			}
			c := DefaultConfig()
			c.Agents = []string{"opencode"}
			if mode == "interrupted" {
				if err := writeJSON(filepath.Join(root(dir), "state.json"), State{Config: c, Builds: []Build{{ID: "orphan", Status: "building", Config: c}}}); err != nil {
					t.Fatal(err)
				}
				m, err = New(dir, nil)
				if err != nil || m.Snapshot().Builds[0].Status != "interrupted" {
					t.Fatal(err)
				}
			} else {
				b, err := m.Build(c)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "cancelled" {
					if err = m.Cancel(b.ID); err != nil {
						t.Fatal(err)
					}
				}
				if result := awaitBuild(t, m, b.ID); result.Status != mode {
					t.Fatal(result)
				}
			}
			if active, err := CurrentImage(dir, ""); err != nil || active != digestA {
				t.Fatal(active, err)
			}
		})
	}
}

func TestExistingImagesArePinnedAndOldTaskSnapshotSurvivesSwitch(t *testing.T) {
	dir := t.TempDir()
	digest := digestA
	m, err := New(dir, func(ctx context.Context, args []string, out io.Writer) error {
		ref := args[len(args)-1]
		if strings.HasPrefix(ref, "sha256:") {
			_, _ = io.WriteString(out, ref)
		} else {
			_, _ = io.WriteString(out, digest)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Import(context.Background(), "registry.example/agent:latest")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Activate(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	oldTask, err := ApplyToProject(dir, domain.Project{})
	if err != nil {
		t.Fatal(err)
	}
	digest = digestB
	second, err := m.Import(context.Background(), "registry.example/agent:latest")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Activate(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}
	newTask, err := ApplyToProject(dir, domain.Project{})
	if err != nil || newTask.Development.Image != digestB || oldTask.Development.Image != digestA {
		t.Fatal(newTask, oldTask, err)
	}
	if _, err = m.Activate(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if current, err := CurrentImage(dir, ""); err != nil || current != digestA {
		t.Fatal("rollback failed", current, err)
	}
}

func TestRecipeRejectsUnsupportedOrSecretBearingArguments(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.Agents = []string{"unknown"} }, func(c *Config) { c.Agents = []string{"codex", "codex"} },
		func(c *Config) { c.ExtraPackages = []string{"--allow-unauthenticated"} }, func(c *Config) { c.ExtraPackages = []string{"curl;touch /tmp/injected"} },
		func(c *Config) { c.BuildArgs["NPM_REGISTRY"] = "https://user:secret@registry.example" }, func(c *Config) { c.BuildArgs["PYPI_INDEX"] = "https://example/simple?token=secret" },
		func(c *Config) { c.BuildArgs["DOCKER_HOST"] = "tcp://other" }, func(c *Config) { c.BuildArgs["CODEX_VERSION"] = "latest;echo bad" },
		func(c *Config) { c.BaseImage = "--privileged" },
	} {
		c := DefaultConfig()
		c.Agents = []string{"codex"}
		change(&c)
		if err := c.Validate(true); err == nil {
			t.Fatal("invalid recipe accepted", c)
		}
	}
}

func TestLogsBoundedAndUnknownPathRejected(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := &limitedLog{file: f}
	input := strings.Repeat("x", logLimit+1000)
	if n, err := w.Write([]byte(input)); err != nil || n != len(input) {
		t.Fatal(n, err)
	}
	info, _ := f.Stat()
	if info.Size() > logLimit+128 {
		t.Fatal("log unbounded")
	}
	m, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Logs("../../active.json"); err == nil {
		t.Fatal("unknown build path accepted")
	}
}
