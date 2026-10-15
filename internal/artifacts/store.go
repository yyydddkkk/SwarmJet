// Package artifacts stores validated execution output outside the Runner Pod.
// References are immutable; callers persist them with the execution record and
// serve reports through Read rather than exposing a Pod or PVC path.
package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

type Kind string

const (
	Result Kind = "result"
	Log    Kind = "log"
	Report Kind = "report"
)

var ErrConflict = errors.New("artifact already contains different content")

// Ref identifies content without depending on the Pod that produced it.
type Ref struct {
	TaskID      string `json:"taskId"`
	ExecutionID string `json:"executionId"`
	Kind        Kind   `json:"kind"`
	Name        string `json:"name"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
}

type Request struct {
	TaskID      string
	ExecutionID string
	Kind        Kind
	Name        string
	// Role is required for results so their outcome can be checked before publication.
	Role string
}

// Store can be backed by an object service in production. Put must be atomic,
// immutable and idempotent for identical content at the same logical key.
type Store interface {
	Put(context.Context, Request, io.Reader) (Ref, error)
	Read(context.Context, Ref) ([]byte, error)
}

// FileStore is a durable local implementation for isolated kind tests. Root
// should be a control-plane volume, never the task PVC or a Runner mount.
type FileStore struct{ Root string }

// DeleteTask removes retained objects after the retention gate has confirmed
// the Runner is stopped and every required report was saved.
func (s FileStore) DeleteTask(ctx context.Context, taskID string) error {
	if s.Root == "" || !domain.IDPattern.MatchString(taskID) {
		return errors.New("invalid artifact task identity")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.Root, "objects", taskID))
}

func limit(k Kind) int64 {
	switch k {
	case Result:
		return 1 << 20
	case Log, Report:
		return 8 << 20
	default:
		return 0
	}
}

func validRequest(r Request) error {
	if !domain.IDPattern.MatchString(r.TaskID) || !domain.IDPattern.MatchString(r.ExecutionID) || !domain.IDPattern.MatchString(r.Name) || limit(r.Kind) == 0 {
		return errors.New("invalid artifact identity")
	}
	if r.Kind == Result && r.Role == "" {
		return errors.New("result role required")
	}
	return nil
}

func (s FileStore) path(r Request) string {
	return filepath.Join(s.Root, "objects", r.TaskID, r.ExecutionID, string(r.Kind), r.Name)
}

func (s FileStore) Put(ctx context.Context, r Request, src io.Reader) (Ref, error) {
	if s.Root == "" || src == nil {
		return Ref{}, errors.New("artifact store root and content required")
	}
	if err := validRequest(r); err != nil {
		return Ref{}, err
	}
	if err := ctx.Err(); err != nil {
		return Ref{}, err
	}
	b, err := io.ReadAll(io.LimitReader(src, limit(r.Kind)+1))
	if err != nil {
		return Ref{}, err
	}
	if int64(len(b)) > limit(r.Kind) {
		return Ref{}, errors.New("artifact exceeds size limit")
	}
	if err = validate(r, b); err != nil {
		return Ref{}, err
	}
	sum := sha256.Sum256(b)
	ref := Ref{TaskID: r.TaskID, ExecutionID: r.ExecutionID, Kind: r.Kind, Name: r.Name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b))}
	p := s.path(r)
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return Ref{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".upload-")
	if err != nil {
		return Ref{}, err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return Ref{}, err
	}
	if closeErr != nil {
		return Ref{}, closeErr
	}
	// Link publishes only a completely written file. EEXIST is a duplicate
	// submission and must match byte-for-byte; it never replaces prior output.
	if err = os.Link(f.Name(), p); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return Ref{}, err
		}
		if _, err = s.Read(ctx, ref); err != nil {
			return Ref{}, ErrConflict
		}
		return ref, nil
	}
	if dir, openErr := os.Open(filepath.Dir(p)); openErr == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return ref, nil
}

func (s FileStore) Read(ctx context.Context, ref Ref) ([]byte, error) {
	if s.Root == "" {
		return nil, errors.New("artifact store root required")
	}
	r := Request{TaskID: ref.TaskID, ExecutionID: ref.ExecutionID, Kind: ref.Kind, Name: ref.Name}
	if ref.Kind == Result {
		r.Role = "read"
	}
	if err := validRequest(r); err != nil || len(ref.SHA256) != 64 || ref.Size < 0 || ref.Size > limit(ref.Kind) {
		return nil, errors.New("invalid artifact reference")
	}
	if _, err := hex.DecodeString(ref.SHA256); err != nil {
		return nil, errors.New("invalid artifact digest")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(s.path(r))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("artifact is not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit(ref.Kind)+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	if int64(len(b)) != ref.Size || hex.EncodeToString(sum[:]) != ref.SHA256 {
		return nil, errors.New("artifact digest mismatch")
	}
	return b, nil
}

func validate(r Request, b []byte) error {
	if r.Kind != Result {
		return nil
	}
	if !json.Valid(b) || len(bytes.TrimSpace(b)) == 0 || bytes.TrimSpace(b)[0] != '{' {
		return errors.New("result must be a JSON object")
	}
	var out domain.AgentResult
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return fmt.Errorf("invalid result: %w", err)
	}
	if strings.TrimSpace(out.Summary) == "" {
		return errors.New("result summary required")
	}
	allowed := map[string]string{"dev": "changed,no_change,blocked", "review": "approved,changes_requested,blocked", "merge": "merged,blocked", "init": "ready,needs_input,blocked"}
	if choices, ok := allowed[r.Role]; ok {
		if !strings.Contains(","+choices+",", ","+out.Outcome+",") {
			return errors.New("invalid result outcome")
		}
	} else {
		return errors.New("invalid result role")
	}
	if out.Outcome == "blocked" && strings.TrimSpace(out.NeedsHelp) == "" {
		return errors.New("blocked result requires needsHelp")
	}
	return nil
}
