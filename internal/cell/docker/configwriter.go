package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/cell/omp"
)

// ConfigWriter installs the platform's omp configuration below omp.ConfigRoot
// inside a running Cell. Implementations must overwrite the previous files and
// leave omp's session database untouched.
type ConfigWriter interface {
	Write(ctx context.Context, container string, files map[string]string) error
}

// DockerConfigWriter writes a tar stream through `docker exec -i`. Files are
// installed mode 0444 so the CLI process can read but not modify them through
// ordinary writes. The managed-path manifest lets the next Start remove stale
// methodology files without deleting omp's state.
type DockerConfigWriter struct {
	Binary string
}

func (w *DockerConfigWriter) Write(ctx context.Context, container string, files map[string]string) error {
	if container == "" {
		return errors.New("cell: config writer requires a container name")
	}
	payload, err := buildConfigTar(files)
	if err != nil {
		return err
	}
	binary := w.Binary
	if binary == "" {
		binary = "docker"
	}
	script := `set -eu
cd ` + omp.ConfigRoot + ` 2>/dev/null || { mkdir -p ` + omp.ConfigRoot + ` && cd ` + omp.ConfigRoot + `; }
if [ -f ` + omp.ManifestName + ` ]; then
  while IFS= read -r p; do
    [ -n "$p" ] && rm -f -- "$p"
  done < ` + omp.ManifestName + `
  rm -f -- ` + omp.ManifestName + `
fi
tar -x -f -`
	cmd := exec.CommandContext(ctx, binary, "exec", "-i", container, "sh", "-c", script)
	cmd.Stdin = bytes.NewReader(payload)
	stdout := newLimitedBuffer(cellOutputCap)
	stderr := newLimitedBuffer(cellOutputCap)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(string(stderr.Bytes()))
		if detail != "" {
			return fmt.Errorf("install omp configuration: %w: %s", err, detail)
		}
		return fmt.Errorf("install omp configuration: %w", err)
	}
	return nil
}

// buildConfigTar renders the files and the managed-path manifest as a tar
// stream rooted at omp.ConfigRoot. Every path is validated before it becomes a
// tar entry.
func buildConfigTar(files map[string]string) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		if name == omp.ManifestName {
			return nil, fmt.Errorf("config file %q is reserved by the runtime", name)
		}
		if err := omp.ValidatePath(name); err != nil {
			return nil, fmt.Errorf("config file: %w", err)
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, name := range names {
		content := files[name]
		header := &tar.Header{
			Name:     name,
			Mode:     0444,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(header); err != nil {
			return nil, fmt.Errorf("write config header: %w", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			return nil, fmt.Errorf("write config content: %w", err)
		}
	}
	manifest := strings.Join(names, "\n")
	if manifest != "" {
		manifest += "\n"
	}
	header := &tar.Header{
		Name:     omp.ManifestName,
		Mode:     0444,
		Size:     int64(len(manifest)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(header); err != nil {
		return nil, fmt.Errorf("write manifest header: %w", err)
	}
	if _, err := tw.Write([]byte(manifest)); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close config archive: %w", err)
	}
	return buf.Bytes(), nil
}
