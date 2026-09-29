package imagebuild

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ReleaseRepository is intentionally fixed: platform releases may only use
// source published by the SwarmJet project.
const ReleaseRepository = "https://github.com/yyydddkkk/SwarmJet.git"

var ErrReleaseSource = errors.New("无法读取发布仓库")

var releaseVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type ReleaseTag struct {
	Tag string `json:"tag"`
}

type ReleaseManifest struct {
	Version   string `json:"version"`
	CommitSHA string `json:"commitSHA"`
	Platform  string `json:"platform"`
	Reference string `json:"reference,omitempty"`
	Digest    string `json:"digest,omitempty"`
}

func (m *Manager) releaseURL() string {
	if m.releaseSourceURL != "" {
		return m.releaseSourceURL // local test fixture only
	}
	return ReleaseRepository
}

func (m *Manager) gitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if m.ReleaseEnv != nil {
		vars, err := m.ReleaseEnv(ctx)
		if err != nil {
			return "", fmt.Errorf("无法读取发布网络配置: %w", err)
		}
		for key, value := range vars {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrReleaseSource, err)
	}
	return string(out), nil
}

// ListReleaseTags reads exact tag refs; no branch, prerelease, or loose
// version string becomes a release candidate.
func (m *Manager) ListReleaseTags(ctx context.Context) ([]ReleaseTag, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := m.gitOutput(ctx, "ls-remote", "--tags", "--refs", m.releaseURL())
	if err != nil {
		return nil, err
	}
	tags := []ReleaseTag{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !commitSHA.MatchString(fields[0]) {
			continue
		}
		tag := strings.TrimPrefix(fields[1], "refs/tags/")
		if fields[1] == "refs/tags/"+tag && releaseVersion.MatchString(tag) {
			tags = append(tags, ReleaseTag{Tag: tag})
		}
	}
	sort.Slice(tags, func(i, j int) bool {
		left, right := strings.Split(tags[i].Tag[1:], "."), strings.Split(tags[j].Tag[1:], ".")
		for part := 0; part < 3; part++ {
			l, lerr := strconv.ParseUint(left[part], 10, 64)
			r, rerr := strconv.ParseUint(right[part], 10, 64)
			if lerr != nil || rerr != nil {
				if len(left[part]) != len(right[part]) {
					return len(left[part]) > len(right[part])
				}
				if left[part] != right[part] {
					return left[part] > right[part]
				}
				continue
			}
			if l != r {
				return l > r
			}
		}
		return false
	})
	return tags, nil
}

// BuildPlatformRelease fetches one exact immutable tag target before handing
// the checked-out tree to the existing serialized BuildKit queue.
func (m *Manager) BuildPlatformRelease(ctx context.Context, tag string) (Build, error) {
	if !releaseVersion.MatchString(tag) {
		return Build{}, errors.New("发布版本必须是 vX.Y.Z 标签")
	}
	m.mu.Lock()
	for _, b := range m.state.Builds {
		if b.Release != nil && b.Release.Version == tag && (b.Status == "building" || b.Status == "cancelling" || b.Status == "succeeded") {
			m.mu.Unlock()
			return b, nil
		}
	}
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	sourceRoot := filepath.Join(m.dir, "release-sources")
	if err := os.MkdirAll(sourceRoot, 0700); err != nil {
		return Build{}, err
	}
	dir, err := os.MkdirTemp(sourceRoot, "release-")
	if err != nil {
		return Build{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, args := range [][]string{
		{"init", "--quiet", dir},
		{"-C", dir, "remote", "add", "origin", m.releaseURL()},
		{"-C", dir, "fetch", "--quiet", "--depth", "1", "origin", "refs/tags/" + tag},
	} {
		if _, err = m.gitOutput(ctx, args...); err != nil {
			return Build{}, err
		}
	}
	sha, err := m.gitOutput(ctx, "-C", dir, "rev-parse", "FETCH_HEAD^{commit}")
	if err != nil {
		return Build{}, err
	}
	sha = strings.TrimSpace(sha)
	if !commitSHA.MatchString(sha) {
		return Build{}, errors.New("发布标签提交 SHA 无效")
	}
	if _, err = m.gitOutput(ctx, "-C", dir, "checkout", "--quiet", "--detach", sha); err != nil {
		return Build{}, err
	}
	manifest := &ReleaseManifest{Version: tag, CommitSHA: sha, Platform: "linux/" + runtime.GOARCH}
	build, err := m.buildPlatform(tag, dir, manifest)
	if err != nil {
		return Build{}, err
	}
	keep = build.Release == manifest
	return build, nil
}
