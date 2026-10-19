package acp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
)

// Process is a stdio transport backed by an OS process: a local CLI, or the
// `docker exec` client that reaches a CLI running inside a Cell container. It
// deliberately keeps the process alive across many turns, matching the
// long-running-CLI model in the ADR.
type Process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *cappedBuffer
	done   chan struct{}

	waitErr error
	once    sync.Once
}

// cappedBuffer keeps the first maxStderrBytes of a stream for diagnostics so a
// chatty CLI cannot exhaust memory.
type cappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

const maxStderrBytes = 64 << 10

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if remaining := maxStderrBytes - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// StartLocal starts an agent on the host and returns a stdio transport for it.
// env entries are appended to the current process environment.
func StartLocal(ctx context.Context, name string, args, env []string, dir string) (*Process, error) {
	if name == "" {
		return nil, errors.New("acp: process name is required")
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Dir = dir
	return startProcess(cmd)
}

// StartDockerExec starts argv inside an existing container and returns the
// container process' stdio. dockerBinary defaults to "docker". env entries are
// passed with `-e`, which is how Cell credentials reach the CLI without being
// baked into the image.
func StartDockerExec(ctx context.Context, dockerBinary, container string, env, argv []string) (*Process, error) {
	return StartDockerExecIn(ctx, dockerBinary, container, "", env, argv)
}

// StartDockerExecIn is StartDockerExec with an explicit working directory
// inside the container. A Cell must always start its CLI in /workspace because
// omp attaches sessions to the cwd.
func StartDockerExecIn(ctx context.Context, dockerBinary, container, workdir string, env, argv []string) (*Process, error) {
	if container == "" {
		return nil, errors.New("acp: docker exec requires a container name")
	}
	if len(argv) == 0 {
		return nil, errors.New("acp: docker exec requires a command")
	}
	if dockerBinary == "" {
		dockerBinary = "docker"
	}
	args := []string{"exec", "-i"}
	if workdir != "" {
		args = append(args, "-w", workdir)
	}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	args = append(args, container)
	args = append(args, argv...)
	return startProcess(exec.CommandContext(ctx, dockerBinary, args...))
}

func startProcess(cmd *exec.Cmd) (*Process, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr := &cappedBuffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &Process{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr, done: make(chan struct{})}
	go func() {
		p.waitErr = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// Read reads the agent's stdout.
func (p *Process) Read(b []byte) (int, error) { return p.stdout.Read(b) }

// Write writes to the agent's stdin.
func (p *Process) Write(b []byte) (int, error) { return p.stdin.Write(b) }

// Close drops stdin and terminates the process. Killing the exec'd process
// leaves a Cell container running so it can be reconnected later.
func (p *Process) Close() error {
	p.once.Do(func() {
		_ = p.stdin.Close()
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Kill()
		}
	})
	<-p.done
	return nil
}

// Wait blocks until the process exits and returns its exit error.
func (p *Process) Wait() error {
	<-p.done
	return p.waitErr
}

// Exited reports whether the process has already exited.
func (p *Process) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Stderr returns the captured stderr tail, useful when the handshake fails.
func (p *Process) Stderr() string { return p.stderr.String() }

// DockerCell describes how the platform reaches an agent inside a Cell: an
// existing long-lived container, the argv to run in it, and the credentials to
// inject through the environment.
type DockerCell struct {
	DockerBinary string
	Container    string
	// Workdir is the in-container cwd. Cells always use /workspace.
	Workdir string
	// Env is passed with `docker exec -e`. Keep credentials here rather than in
	// the image, mirroring internal/agent's configuration snapshot.
	Env []string
	// Argv is the in-container command, for example {"omp", "acp"}.
	Argv []string
}

// Connect starts Argv inside the container and returns a client positioned to
// call Initialize.
func (c DockerCell) Connect(ctx context.Context, opts ...Option) (*Client, error) {
	proc, err := StartDockerExecIn(ctx, c.DockerBinary, c.Container, c.Workdir, c.Env, c.Argv)
	if err != nil {
		return nil, err
	}
	return NewClient(proc, opts...), nil
}
