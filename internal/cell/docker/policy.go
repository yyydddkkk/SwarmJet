package docker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// DefaultContainerUser is the non-root uid:gid a Cell CLI runs as. Running the
// CLI as root makes the host-side workspace belong to root; 1000:1000 matches
// the image's default user and can be overridden per Runtime.
const DefaultContainerUser = "1000:1000"

// ContainerPolicyArgs returns the docker create flags shared by Cell
// containers: network, resource limits and isolation. Cells enforce the same
// policy as the platform's other container workloads.
func ContainerPolicyArgs(config domain.AgentConfig) []string {
	args := []string{}
	if config.DockerNetwork != "" {
		args = append(args, "--network", config.DockerNetwork)
	}
	if config.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(config.MemoryMB)+"m")
	}
	if config.CPUs > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(config.CPUs, 'f', -1, 64))
	}
	if config.PidsLimit > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(config.PidsLimit))
	}
	return append(args, "--cap-drop=ALL", "--security-opt=no-new-privileges")
}

// ContainerUser returns the default non-root uid:gid used for Cell CLIs so
// files created in a mounted workspace stay accessible without belonging to
// root on the host.
func ContainerUser() string {
	return DefaultContainerUser
}

// ContainerUserFor validates a configured uid:gid and returns it in canonical
// form. An empty value falls back to ContainerUser, so a bad override fails
// before Docker is invoked instead of surfacing as an opaque container error.
func ContainerUserFor(configured string) (string, error) {
	uid, gid, err := parseContainerUser(configured)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(uid) + ":" + strconv.Itoa(gid), nil
}

func parseContainerUser(raw string) (int, int, error) {
	spec := strings.TrimSpace(raw)
	if spec == "" {
		spec = ContainerUser()
	}
	uidText, gidText, ok := strings.Cut(spec, ":")
	if !ok {
		return 0, 0, fmt.Errorf("cell: container user %q must be uid:gid", raw)
	}
	uid, err := strconv.Atoi(strings.TrimSpace(uidText))
	if err != nil || uid < 0 {
		return 0, 0, fmt.Errorf("cell: invalid container uid in %q", raw)
	}
	gid, err := strconv.Atoi(strings.TrimSpace(gidText))
	if err != nil || gid < 0 {
		return 0, 0, fmt.Errorf("cell: invalid container gid in %q", raw)
	}
	return uid, gid, nil
}
