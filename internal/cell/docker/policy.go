package docker

import (
	"os"
	"strconv"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

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

// ContainerUser is the uid:gid pair used for CLI containers so files created
// in a mounted workspace remain accessible to the platform process.
func ContainerUser() string {
	return strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
}
