package k8sintegration

import (
	"testing"

	"swarmjet/internal/k8srunner"
)

func TestManagedInitUsesFencedRestrictedJob(t *testing.T) {
	c := ManagedInitContract{GeneratedDir: "/workspace/generated", Execution: k8srunner.Spec{TaskID: "task", ExecutionID: "init", Generation: 1, Image: image, PVC: "task-pvc", Resources: k8srunner.Resources{CPURequest: "50m", CPULimit: "200m", MemoryRequest: "64Mi", MemoryLimit: "256Mi"}, Command: []string{"/runner", "init"}, SecretName: "model-only"}}
	job, err := BuildManagedInitJob(c)
	if err != nil {
		t.Fatal(err)
	}
	spec := job["spec"].(map[string]any)
	if spec["backoffLimit"] != 0 {
		t.Fatal("init Job retries enabled")
	}
	pod := spec["template"].(map[string]any)["spec"].(map[string]any)
	if pod["automountServiceAccountToken"] != false {
		t.Fatal("init Runner has API token")
	}
	container := pod["containers"].([]any)[0].(map[string]any)
	if container["image"] != image || container["securityContext"].(map[string]any)["readOnlyRootFilesystem"] != true {
		t.Fatal("init Runner changed execution restriction")
	}
	c.GeneratedDir = "/tmp/out"
	if _, err = BuildManagedInitJob(c); err == nil {
		t.Fatal("output outside task PVC accepted")
	}
}
