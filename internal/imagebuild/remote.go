package imagebuild

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"swarmjet/internal/domain"
	"swarmjet/internal/registryconfig"
	"syscall"
)

// Prepared is ephemeral. Credential material is never part of the build record.
type Prepared struct {
	Env      map[string]string
	Registry registryconfig.Access
}
type preparedKey struct{}

func RemoteRunner(endpoint string) Runner {
	return func(ctx context.Context, args []string, out io.Writer) error {
		if len(args) == 0 || args[0] != "build" {
			return docker(ctx, args, out)
		}
		p, _ := ctx.Value(preparedKey{}).(Prepared)
		auth, e := p.Registry.DockerConfig("")
		if e != nil {
			return e
		}
		defer os.RemoveAll(auth)
		metadata := ""
		tag := ""
		for i, v := range args {
			if v == "--tag" && i+1 < len(args) {
				tag = args[i+1]
			}
			if v == "--metadata-file" && i+1 < len(args) {
				metadata = args[i+1]
			}
		}
		if tag == "" || metadata == "" {
			return fmt.Errorf("remote build requires registry output")
		}
		// An ephemeral Buildx client points at the independently managed BuildKit.
		name := "sj-" + filepath.Base(filepath.Dir(metadata))
		env := []string{}
		for _, k := range []string{"PATH", "HOME", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
			if v := os.Getenv(k); v != "" {
				env = append(env, k+"="+v)
			}
		}
		for k, v := range p.Env {
			env = append(env, k+"="+v)
		}
		env = append(env, "DOCKER_CONFIG="+auth)
		run := func(a []string, w io.Writer) error {
			cmd := exec.CommandContext(ctx, "docker", a...)
			cmd.Env = env
			cmd.Stdout = w
			cmd.Stderr = w
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
			cmd.Cancel = func() error {
				if cmd.Process == nil {
					return nil
				}
				return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			if cmd.Run() != nil {
				return fmt.Errorf("BuildKit operation failed")
			}
			return nil
		}
		create := []string{"buildx", "create", "--name", name, "--driver", "remote", endpoint}
		for _, key := range []string{"cacert", "cert", "key", "servername"} {
			if v := os.Getenv("BUILDKIT_" + strings.ToUpper(key)); v != "" {
				create = append(create, "--driver-opt", key+"="+v)
			}
		}
		if e = run(create, io.Discard); e != nil {
			return e
		}
		a := append([]string{"buildx", "build", "--builder", name, "--push", "--provenance=false"}, args[1:]...)
		// Pass values through environment, not argv or persisted Dockerfile ARGs.
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
			a = append(a[:len(a)-1], append([]string{"--build-arg", key}, a[len(a)-1])...)
		}
		return run(a, out)
	}
}
func readPublished(metadata, repository string) (string, error) {
	b, e := os.ReadFile(metadata)
	if e != nil {
		return "", e
	}
	var v map[string]json.RawMessage
	if e = json.Unmarshal(b, &v); e != nil {
		return "", e
	}
	var digest string
	if e = json.Unmarshal(v["containerimage.digest"], &digest); e != nil {
		return "", e
	}
	ref := repository + "@" + digest
	if !domain.RepositoryDigest.MatchString(ref) {
		return "", fmt.Errorf("invalid published digest")
	}
	return ref, nil
}
