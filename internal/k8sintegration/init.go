package k8sintegration

import (
	"errors"

	"swarmjet/internal/k8srunner"
)

// ManagedInitContract uses the same one-attempt, fenced Job shape as agent
// execution. The caller must obtain authority and use Controller.Create; this
// builder neither grants platform write credentials nor marks init complete.
type ManagedInitContract struct {
	Execution k8srunner.Spec
	// GeneratedDir is a task PVC path consumed by the trusted init verifier.
	GeneratedDir string
}

func BuildManagedInitJob(c ManagedInitContract) (k8srunner.Job, error) {
	if c.GeneratedDir != "/workspace/generated" {
		return nil, errors.New("managed init output must use the task PVC generated directory")
	}
	job, err := k8srunner.BuildJob(c.Execution)
	if err != nil {
		return nil, err
	}
	metadata := job["metadata"].(map[string]any)
	labels := metadata["labels"].(map[string]any)
	labels["swarmjet.io/role"] = "init"
	spec := job["spec"].(map[string]any)
	template := spec["template"].(map[string]any)
	templateMetadata := template["metadata"].(map[string]any)
	templateLabels := templateMetadata["labels"].(map[string]any)
	templateLabels["swarmjet.io/role"] = "init"
	podSpec := template["spec"].(map[string]any)
	containers := podSpec["containers"].([]any)
	container := containers[0].(map[string]any)
	env := container["env"].([]any)
	container["env"] = append(env, map[string]any{"name": "SWARMJET_ROLE", "value": "init"}, map[string]any{"name": "SWARMJET_GENERATED_DIR", "value": c.GeneratedDir})
	return job, nil
}
