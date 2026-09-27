# Kubernetes integration fixture

Build with `docker build -f deploy/k8s-runner/integration-fixture/Dockerfile -t swarmjet-integration-fixture:local .`, load the image into the isolated kind cluster, and use its manifest digest in every `k8sintegration.Container` and `k8sintegration.Check`. The fixture has three modes:

- `/fixture server 8080` serves `/health` on port 8080.
- `/fixture check 8080` verifies the service at `SWARMJET_SERVICE_HOST:8080` and emits a short report.
- `/fixture sleep` holds a check Job open for cancellation testing.

For a multi-container test, add a second service container with `/fixture server 8081` and a second check with `/fixture check 8081`. For failure, point a check at an unused port. The manager creates a NetworkPolicy before a Service and Pod, waits for readiness, then runs one zero-retry Job per check. `RunAndPersist` calls the report callback before deleting resources; if report storage fails, resources stay available for retry and diagnosis. Apply `../integration-rbac.yaml` to grant the trusted controller the additional Pod, Service, and NetworkPolicy permissions. The Runner ServiceAccount still has no API token.

The service and check images have no shell, Docker socket, or platform credential. Workspace PVC mounts are read-only and `/tmp` is a bounded `emptyDir`. The namespace needs a CNI that enforces NetworkPolicy; verify deny and allow behavior with the probes in `../README.md` before using untrusted repository code.

## Isolated kind observation (2026-09-27)

With Cilium enforcing the task policy, one service container passed its required check; two service containers passed two checks. A check aimed at an unused port returned a failed report. Cancelling a running check removed its Job and service resources. After each run, a label query found no remaining integration resources. The disposable cluster and test image were deleted. This exercises Kubernetes resources, but the package is not yet wired into the project configuration or task Workflow.
