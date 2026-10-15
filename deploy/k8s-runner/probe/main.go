// Command probe prints a deterministic Runner Job for isolated kind checks.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/yyydddkkk/SwarmJet/internal/k8srunner"
)

func main() {
	image := flag.String("image", "", "runner image pinned by digest")
	flag.Parse()
	spec := k8srunner.Spec{ExecutionID: "live-probe", Generation: 1, TaskID: "live-probe", Image: *image, PVC: k8srunner.PVCName("live-probe"), Resources: k8srunner.Resources{CPURequest: "100m", CPULimit: "500m", MemoryRequest: "64Mi", MemoryLimit: "256Mi"}, Command: []string{"/runner"}}
	job, err := k8srunner.BuildJob(spec)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(job); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
