// The deterministic runner is a first vertical-slice probe. It does not run
// repository code or perform remote writes. The platform validates its result
// and current execution right before treating the task as complete.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

var safeID = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

type result struct {
	SchemaVersion int    `json:"schemaVersion"`
	ExecutionID   string `json:"executionId"`
	Generation    int64  `json:"generation"`
	TaskID        string `json:"taskId"`
	Status        string `json:"status"`
	InputSHA256   string `json:"inputSha256"`
}

func run() error {
	executionID, taskID := os.Getenv("SWARMJET_EXECUTION_ID"), os.Getenv("SWARMJET_TASK_ID")
	if !safeID.MatchString(executionID) || !safeID.MatchString(taskID) {
		return errors.New("invalid execution or task ID")
	}
	generation, err := strconv.ParseInt(os.Getenv("SWARMJET_GENERATION"), 10, 64)
	if err != nil || generation < 1 {
		return errors.New("invalid execution generation")
	}
	workspace := os.Getenv("SWARMJET_WORKSPACE")
	if workspace == "" {
		workspace = "/workspace"
	}
	inputPath := filepath.Join(workspace, "input.txt")
	input, err := os.ReadFile(inputPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read deterministic input: %w", err)
		}
		input = []byte("swarmjet-deterministic-runner-v1\n")
	}
	digest := sha256.Sum256(input)
	output := result{SchemaVersion: 1, ExecutionID: executionID, Generation: generation, TaskID: taskID, Status: "succeeded", InputSHA256: hex.EncodeToString(digest[:])}
	data, err := json.Marshal(output)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Join(workspace, "results", executionID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	target := filepath.Join(dir, strconv.FormatInt(generation, 10)+".json")
	f, err := os.CreateTemp(dir, ".result-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), target); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
