package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Checkpoint describes the exact CLI session files and workspace directories
// retained on a task PVC. It is evidence for takeover, not execution authority;
// the database lease must be checked separately after the old Pod has stopped.
type Checkpoint struct {
	TaskID        string         `json:"taskId"`
	ExecutionID   string         `json:"executionId"`
	SessionID     string         `json:"sessionId"`
	LeaseVersion  int64          `json:"leaseVersion"`
	WorkspaceDirs []string       `json:"workspaceDirs"`
	SessionFiles  []FileEvidence `json:"sessionFiles"`
}

type FileEvidence struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Identity struct {
	TaskID       string
	ExecutionID  string
	SessionID    string
	LeaseVersion int64
}

// CaptureCheckpoint records files selected by the adapter for exact CLI resume.
// The adapter must include every file needed by its session format. An empty or
// missing session is rejected rather than silently starting a fresh session.
func CaptureCheckpoint(pvcRoot string, id Identity, workspaceDirs, sessionFiles []string) (Checkpoint, error) {
	cp := Checkpoint{TaskID: id.TaskID, ExecutionID: id.ExecutionID, SessionID: id.SessionID, LeaseVersion: id.LeaseVersion, WorkspaceDirs: append([]string(nil), workspaceDirs...)}
	if err := validIdentity(id); err != nil {
		return Checkpoint{}, err
	}
	if len(workspaceDirs) == 0 || len(sessionFiles) == 0 {
		return Checkpoint{}, errors.New("checkpoint requires workspace and session files")
	}
	seen := map[string]bool{}
	for _, dir := range workspaceDirs {
		if err := checkDirectory(pvcRoot, dir); err != nil {
			return Checkpoint{}, err
		}
	}
	for _, name := range sessionFiles {
		if seen[name] {
			return Checkpoint{}, errors.New("duplicate session file")
		}
		seen[name] = true
		f, err := inspectFile(pvcRoot, name)
		if err != nil {
			return Checkpoint{}, err
		}
		cp.SessionFiles = append(cp.SessionFiles, f)
	}
	return cp, nil
}

// ValidateCheckpoint checks remounted PVC content before a new Pod may resume.
// The caller supplies the prior lease identity, then acquires a newer lease.
func ValidateCheckpoint(pvcRoot string, expected Identity, cp Checkpoint) error {
	if err := validIdentity(expected); err != nil {
		return err
	}
	if cp.TaskID != expected.TaskID || cp.ExecutionID != expected.ExecutionID || cp.SessionID != expected.SessionID || cp.LeaseVersion != expected.LeaseVersion {
		return errors.New("checkpoint identity mismatch")
	}
	if len(cp.WorkspaceDirs) == 0 || len(cp.SessionFiles) == 0 {
		return errors.New("checkpoint incomplete")
	}
	for _, dir := range cp.WorkspaceDirs {
		if err := checkDirectory(pvcRoot, dir); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, want := range cp.SessionFiles {
		if seen[want.Path] || want.Size < 0 || len(want.SHA256) != 64 {
			return errors.New("invalid session evidence")
		}
		seen[want.Path] = true
		got, err := inspectFile(pvcRoot, want.Path)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("session file %q changed", want.Path)
		}
	}
	return nil
}

func validIdentity(id Identity) error {
	if !validID(id.TaskID) || !validID(id.ExecutionID) || strings.TrimSpace(id.SessionID) == "" || len(id.SessionID) > 512 || id.LeaseVersion <= 0 {
		return errors.New("invalid checkpoint identity")
	}
	return nil
}

func validID(s string) bool {
	if len(s) == 0 || len(s) > 80 || !(s[0] >= 'a' && s[0] <= 'z' || s[0] >= 'A' && s[0] <= 'Z' || s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for _, c := range s {
		if c < 'a' || c > 'z' {
			if c < 'A' || c > 'Z' {
				if c < '0' || c > '9' {
					if c != '-' && c != '_' {
						return false
					}
				}
			}
		}
	}
	return true
}

func validRelative(name string) bool {
	return name != "" && !strings.Contains(name, "\\") && !strings.HasPrefix(name, "/") && path.Clean(name) == name && name != "." && name != ".." && !strings.HasPrefix(name, "../")
}

func safePath(root, name string) (string, error) {
	if !filepath.IsAbs(root) || !validRelative(name) {
		return "", errors.New("invalid PVC relative path")
	}
	// Reject symlinks at every component. The PVC must be quiescent while
	// validating, as guaranteed by the caller stopping the previous Pod.
	current := root
	for _, part := range strings.Split(name, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("checkpoint path contains symlink")
		}
	}
	return current, nil
}

func checkDirectory(root, name string) error {
	p, err := safePath(root, name)
	if err != nil {
		return err
	}
	info, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("checkpoint workspace is not a directory")
	}
	return nil
}

func inspectFile(root, name string) (FileEvidence, error) {
	p, err := safePath(root, name)
	if err != nil {
		return FileEvidence{}, err
	}
	f, err := os.Open(p)
	if err != nil {
		return FileEvidence{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return FileEvidence{}, errors.New("session evidence is not a regular file")
	}
	if info.Size() > 32<<20 {
		return FileEvidence{}, errors.New("session evidence exceeds size limit")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, 32<<20+1))
	if err != nil {
		return FileEvidence{}, err
	}
	if n != info.Size() {
		return FileEvidence{}, errors.New("session evidence changed while reading")
	}
	return FileEvidence{Path: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}
