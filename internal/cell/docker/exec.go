package docker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
)

// Result is the outcome of one docker invocation. A non-zero ExitCode is not a
// Go error: callers decide whether it means "not found" or a failure.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// Executor runs one-shot docker commands. Tests replace it with a recorder.
type Executor interface {
	Run(ctx context.Context, args []string, env []string) (Result, error)
}

// execExecutor is the production Executor.
type execExecutor struct {
	Binary string
}

func (e execExecutor) binary() string {
	if e.Binary != "" {
		return e.Binary
	}
	return "docker"
}

func (e execExecutor) Run(ctx context.Context, args []string, env []string) (Result, error) {
	if len(args) == 0 {
		return Result{}, errors.New("docker: empty command")
	}
	cmd := exec.CommandContext(ctx, e.binary(), args...)
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err == nil {
		return res, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	}
	return res, err
}
