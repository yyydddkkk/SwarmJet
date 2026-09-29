package imagebuild

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"swarmjet/internal/domain"
	"testing"
)

func localReleaseRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	command := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	command("init", "--quiet")
	command("config", "user.name", "Test")
	command("config", "user.email", "test@example.invalid")
	if err := os.MkdirAll(filepath.Join(dir, "deploy"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deploy", "Dockerfile"), []byte("FROM scratch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command("add", ".")
	command("commit", "--quiet", "-m", "release")
	sha := command("rev-parse", "HEAD")
	command("tag", "v1.2.3")
	command("tag", "v1.2.3-rc1")
	return dir, sha
}

func TestPlatformReleasePinsNetworkBeforeGitAndBuild(t *testing.T) {
	repo, _ := localReleaseRepo(t)
	m, err := New(t.TempDir(), func(_ context.Context, args []string, _ io.Writer) error {
		for i, arg := range args {
			if arg == "--metadata-file" && i+1 < len(args) {
				return os.WriteFile(args[i+1], []byte(`{"containerimage.digest":"`+digestA+`"}`), 0600)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.releaseSourceURL = repo
	currentVersion, resolveCount := 1, 0
	m.ReleaseEnv = func(context.Context) (Prepared, error) {
		resolveCount++
		p := Prepared{Env: map[string]string{"HTTP_PROXY": ""}, NetworkVersion: domain.NetworkVersion{ID: "build-proxy", Version: currentVersion}}
		currentVersion = 2 // Setting changes while the release source is fetched.
		return p, nil
	}
	m.ResolvePlatform = func(ctx context.Context, _ string) (Prepared, string, error) {
		p, ok := PinnedReleaseNetwork(ctx)
		if !ok || p.NetworkVersion.Version != 1 {
			t.Errorf("release network was not pinned: %+v", p.NetworkVersion)
		}
		return p, "registry.test/swarmjet/platform", nil
	}
	b, err := m.BuildPlatformRelease(context.Background(), "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if result := awaitBuild(t, m, b.ID); result.Status != "succeeded" {
		t.Fatal(result)
	}
	if resolveCount != 1 || b.Config.NetworkVersion != (domain.NetworkVersion{ID: "build-proxy", Version: 1}) {
		t.Fatalf("resolved %d times, build version %+v", resolveCount, b.Config.NetworkVersion)
	}
}

func TestUnavailableReleaseSourceNeverBuildsCurrentTree(t *testing.T) {
	m, err := New(t.TempDir(), func(context.Context, []string, io.Writer) error {
		t.Fatal("BuildKit should not start without release source")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.releaseSourceURL = filepath.Join(t.TempDir(), "missing")
	_, err = m.ListReleaseTags(context.Background())
	if !errors.Is(err, ErrReleaseSource) {
		t.Fatal(err)
	}
	_, err = m.BuildPlatformRelease(context.Background(), "v1.2.3")
	if !errors.Is(err, ErrReleaseSource) || len(m.Snapshot().Builds) != 0 {
		t.Fatal(err, m.Snapshot().Builds)
	}
}

func TestReleaseTagsSortByVersion(t *testing.T) {
	repo, _ := localReleaseRepo(t)
	for _, tag := range []string{"v2.0.0", "v10.0.0"} {
		if output, err := exec.Command("git", "-C", repo, "tag", tag).CombinedOutput(); err != nil {
			t.Fatalf("tag %s: %s %v", tag, output, err)
		}
	}
	m, err := New(t.TempDir(), func(context.Context, []string, io.Writer) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	m.releaseSourceURL = repo
	tags, err := m.ListReleaseTags(context.Background())
	if err != nil || len(tags) != 3 || tags[0].Tag != "v10.0.0" || tags[1].Tag != "v2.0.0" {
		t.Fatalf("tags=%+v err=%v", tags, err)
	}
}

func TestPlatformReleaseUsesPinnedTagAndPublishedDigest(t *testing.T) {
	repo, sha := localReleaseRepo(t)
	m, err := New(t.TempDir(), func(_ context.Context, args []string, _ io.Writer) error {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--build-arg RELEASE_VERSION=v1.2.3") ||
			!strings.Contains(joined, "--build-arg RELEASE_COMMIT="+sha) {
			t.Errorf("release build args absent: %v", args)
		}
		for i, arg := range args {
			if arg == "--metadata-file" && i+1 < len(args) {
				return os.WriteFile(args[i+1], []byte(`{"containerimage.digest":"`+digestA+`"}`), 0600)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.releaseSourceURL = repo
	m.ResolvePlatform = func(context.Context, string) (Prepared, string, error) {
		return Prepared{}, "registry.test/swarmjet/platform", nil
	}
	tags, err := m.ListReleaseTags(context.Background())
	if err != nil || len(tags) != 1 || tags[0].Tag != "v1.2.3" {
		t.Fatal(tags, err)
	}
	if _, err := m.BuildPlatformRelease(context.Background(), "v1.2.3-rc1"); err == nil {
		t.Fatal("prerelease tag accepted")
	}
	b, err := m.BuildPlatformRelease(context.Background(), "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	finished := awaitBuild(t, m, b.ID)
	if finished.Status != "succeeded" || finished.Release == nil || finished.Release.CommitSHA != sha ||
		finished.Release.Digest != digestA || finished.Release.Reference != "registry.test/swarmjet/platform@"+digestA {
		t.Fatal(finished)
	}
	again, err := m.BuildPlatformRelease(context.Background(), "v1.2.3")
	if err != nil || again.ID != b.ID {
		t.Fatal(again, err)
	}
}

func TestFailedPlatformReleaseCanRetrySameUnpublishedTag(t *testing.T) {
	repo, _ := localReleaseRepo(t)
	attempts := 0
	m, err := New(t.TempDir(), func(_ context.Context, args []string, _ io.Writer) error {
		attempts++
		if attempts == 1 {
			return errors.New("temporary BuildKit failure")
		}
		for i, arg := range args {
			if arg == "--metadata-file" && i+1 < len(args) {
				return os.WriteFile(args[i+1], []byte(`{"containerimage.digest":"`+digestA+`"}`), 0600)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.releaseSourceURL = repo
	m.ResolvePlatform = func(context.Context, string) (Prepared, string, error) {
		return Prepared{}, "registry.test/swarmjet/platform", nil
	}
	first, err := m.BuildPlatformRelease(context.Background(), "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if result := awaitBuild(t, m, first.ID); result.Status != "failed" {
		t.Fatal(result)
	}
	second, err := m.BuildPlatformRelease(context.Background(), "v1.2.3")
	if err != nil || second.ID == first.ID {
		t.Fatalf("retry=%+v err=%v", second, err)
	}
	if result := awaitBuild(t, m, second.ID); result.Status != "succeeded" {
		t.Fatal(result)
	}
}
