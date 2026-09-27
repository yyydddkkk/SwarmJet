package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunWritesAtomicResult(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SWARMJET_WORKSPACE", dir)
	t.Setenv("SWARMJET_EXECUTION_ID", "exec-a")
	t.Setenv("SWARMJET_TASK_ID", "task-a")
	t.Setenv("SWARMJET_GENERATION", "2")
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "results", "exec-a", "2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("hello"))
	if got.Status != "succeeded" || got.Generation != 2 || got.InputSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("bad result: %+v", got)
	}
}

func TestRunUsesBuiltInInputOnFreshWorkspace(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SWARMJET_WORKSPACE", dir)
	t.Setenv("SWARMJET_EXECUTION_ID", "exec-b")
	t.Setenv("SWARMJET_TASK_ID", "task-b")
	t.Setenv("SWARMJET_GENERATION", "1")
	if err := run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "results", "exec-b", "1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got result
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("swarmjet-deterministic-runner-v1\n"))
	if got.InputSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected built-in input digest: %s", got.InputSHA256)
	}
}
