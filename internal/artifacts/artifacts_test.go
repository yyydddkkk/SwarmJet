package artifacts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStoreImmutableAndVerified(t *testing.T) {
	ctx := context.Background()
	s := FileStore{Root: t.TempDir()}
	r := Request{TaskID: "task-1", ExecutionID: "exec-1", Kind: Result, Name: "result", Role: "dev"}
	good := `{"outcome":"changed","summary":"done"}`
	ref, err := s.Put(ctx, r, strings.NewReader(good))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Put(ctx, r, strings.NewReader(good)); err != nil {
		t.Fatal("idempotent write", err)
	}
	if _, err = s.Put(ctx, r, strings.NewReader(`{"outcome":"changed","summary":"different"}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting result accepted: %v", err)
	}
	if b, err := s.Read(ctx, ref); err != nil || string(b) != good {
		t.Fatalf("read: %q %v", b, err)
	}
	if err := os.WriteFile(s.path(r), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, ref); err == nil {
		t.Fatal("tampered result accepted")
	}
}

func TestFileStoreRejectsInvalidContentAndIdentity(t *testing.T) {
	s := FileStore{Root: t.TempDir()}
	r := Request{TaskID: "task", ExecutionID: "exec", Kind: Result, Name: "result", Role: "dev"}
	for _, body := range []string{`[]`, `{"outcome":"changed"}`, `{"outcome":"blocked","summary":"stuck"}`, `{"outcome":"approved","summary":"wrong role"}`} {
		if _, err := s.Put(context.Background(), r, strings.NewReader(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	r.Kind, r.Name = Report, "../escape"
	if _, err := s.Put(context.Background(), r, strings.NewReader("report")); err == nil {
		t.Fatal("path traversal accepted")
	}
	r.Name = "verification"
	if _, err := s.Put(context.Background(), r, strings.NewReader("report")); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointRequiresExactRemountedSession(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "home", "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "workspace"), 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "home", "sessions", "s1.json")
	if err := os.WriteFile(file, []byte(`{"id":"s1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	id := Identity{TaskID: "task", ExecutionID: "exec", SessionID: "s1", LeaseVersion: 3}
	cp, err := CaptureCheckpoint(root, id, []string{"workspace"}, []string{"home/sessions/s1.json"})
	if err != nil || ValidateCheckpoint(root, id, cp) != nil {
		t.Fatalf("valid checkpoint rejected: %v", err)
	}
	other := id
	other.SessionID = "s2"
	if ValidateCheckpoint(root, other, cp) == nil {
		t.Fatal("different session accepted")
	}
	if err := os.WriteFile(file, []byte(`{"id":"s2"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if ValidateCheckpoint(root, id, cp) == nil {
		t.Fatal("changed session accepted")
	}
}

func TestCheckpointRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "workspace"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "session")); err != nil {
		t.Fatal(err)
	}
	id := Identity{TaskID: "task", ExecutionID: "exec", SessionID: "s1", LeaseVersion: 1}
	if _, err := CaptureCheckpoint(root, id, []string{"workspace"}, []string{"session/s1"}); err == nil {
		t.Fatal("symlinked session accepted")
	}
}
