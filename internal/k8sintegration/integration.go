// Package k8sintegration runs task-scoped verification without Docker Compose.
// It owns Kubernetes resources only; the caller owns durable task state and
// persists reports before retention removes the task workspace.
package k8sintegration

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var ErrNotFound = errors.New("Kubernetes resource not found")
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var pinnedImage = regexp.MustCompile(`^[^\s]+@sha256:[0-9a-f]{64}$`)

type Resources struct{ CPURequest, CPULimit, MemoryRequest, MemoryLimit string }
type Container struct {
	Name      string
	Image     string
	Command   []string
	Port      int
	Resources Resources
}
type Check struct {
	Name           string
	Image          string
	Command        []string
	Resources      Resources
	TimeoutSeconds int64
}
type Spec struct {
	TaskID                string
	RunID                 string
	PVC                   string
	StartupTimeoutSeconds int64
	Services              []Container
	Checks                []Check
}

func validName(s string) bool { return len(s) > 0 && len(s) <= 40 && dnsLabel.MatchString(s) }
func validResources(r Resources) bool {
	return r.CPURequest != "" && r.CPULimit != "" && r.MemoryRequest != "" && r.MemoryLimit != ""
}
func validCommand(args []string) bool {
	if len(args) == 0 || args[0] == "" {
		return false
	}
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return false
		}
	}
	return true
}
func (s Spec) Validate() error {
	if !validName(s.TaskID) || !validName(s.RunID) || !validName(s.PVC) || s.StartupTimeoutSeconds < 1 || s.StartupTimeoutSeconds > 3600 || len(s.Services) == 0 || len(s.Checks) == 0 || len(s.Services) > 8 || len(s.Checks) > 32 {
		return errors.New("invalid integration identity or size")
	}
	seen := map[string]bool{}
	ports := map[int]bool{}
	for _, service := range s.Services {
		if !validName(service.Name) || seen[service.Name] || !pinnedImage.MatchString(service.Image) || !validCommand(service.Command) || !validResources(service.Resources) || service.Port < 1 || service.Port > 65535 || ports[service.Port] {
			return errors.New("invalid integration service")
		}
		seen[service.Name] = true
		ports[service.Port] = true
	}
	seen = map[string]bool{}
	for _, check := range s.Checks {
		if !validName(check.Name) || seen[check.Name] || !pinnedImage.MatchString(check.Image) || !validCommand(check.Command) || !validResources(check.Resources) || check.TimeoutSeconds < 1 || check.TimeoutSeconds > 3600 {
			return errors.New("invalid integration check")
		}
		seen[check.Name] = true
	}
	return nil
}

type Resource struct {
	Kind string
	Name string
	Body map[string]any
}
type Status struct {
	Exists, Ready, Complete, Failed bool
	PodIP, Reason                   string
}
type Client interface {
	Create(context.Context, Resource) error
	Observe(context.Context, string, string) (Status, error)
	JobLog(context.Context, string) ([]byte, error)
	Delete(context.Context, string, string) error
}

type Result struct {
	Name   string
	Passed bool
	Reason string
	Report []byte
}

type Manager struct {
	Client       Client
	PollInterval time.Duration
}

func (m Manager) poll() time.Duration {
	if m.PollInterval > 0 {
		return m.PollInterval
	}
	return 500 * time.Millisecond
}

// Start creates deny-first network policy, service discovery, and one Pod with
// one or more containers. A Pod failure is an environment failure; no check is
// submitted until all containers are Ready.
func (m Manager) Start(ctx context.Context, s Spec) error {
	if m.Client == nil {
		return errors.New("Kubernetes integration client required")
	}
	resources, err := Build(s)
	if err != nil {
		return err
	}
	for _, resource := range resources[:3] {
		if err = m.Client.Create(ctx, resource); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(s.StartupTimeoutSeconds)*time.Second)
	defer cancel()
	for {
		state, e := m.Client.Observe(ctx, "Pod", podName(s))
		if e != nil {
			return e
		}
		if state.Failed {
			return fmt.Errorf("integration Pod failed: %s", state.Reason)
		}
		if state.Ready && state.PodIP != "" {
			return nil
		}
		if err = wait(ctx, m.poll()); err != nil {
			return err
		}
	}
}

func (m Manager) Verify(ctx context.Context, s Spec) ([]Result, error) {
	if m.Client == nil {
		return nil, errors.New("Kubernetes integration client required")
	}
	state, err := m.Client.Observe(ctx, "Pod", podName(s))
	if err != nil {
		return nil, err
	}
	if !state.Ready || state.PodIP == "" {
		return nil, errors.New("integration services are not ready")
	}
	var results []Result
	for _, check := range s.Checks {
		job, e := buildCheckJob(s, check, state.PodIP)
		if e != nil {
			return results, e
		}
		if e = m.Client.Create(ctx, job); e != nil {
			return results, e
		}
		for {
			status, e := m.Client.Observe(ctx, "Job", job.Name)
			if e != nil {
				return results, e
			}
			if status.Complete || status.Failed {
				log, e := m.Client.JobLog(ctx, job.Name)
				if e != nil {
					return results, fmt.Errorf("collect %s report: %w", check.Name, e)
				}
				results = append(results, Result{Name: check.Name, Passed: status.Complete, Reason: status.Reason, Report: log})
				break
			}
			if e = wait(ctx, m.poll()); e != nil {
				return results, e
			}
		}
	}
	return results, nil
}

// Stop is idempotent. The policy remains until every Pod is gone, so a
// terminating service never becomes unrestricted during cleanup.
func (m Manager) Stop(ctx context.Context, s Spec) error {
	if m.Client == nil {
		return errors.New("Kubernetes integration client required")
	}
	if err := s.Validate(); err != nil {
		return err
	}
	for _, check := range s.Checks {
		if err := m.Client.Delete(ctx, "Job", jobName(s, check)); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	if err := m.Client.Delete(ctx, "Pod", podName(s)); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	for {
		allGone := true
		for _, check := range s.Checks {
			gone, err := m.gone(ctx, "Job", jobName(s, check))
			if err != nil {
				return err
			}
			allGone = allGone && gone
		}
		gone, err := m.gone(ctx, "Pod", podName(s))
		if err != nil {
			return err
		}
		allGone = allGone && gone
		if allGone {
			break
		}
		if err := wait(ctx, m.poll()); err != nil {
			return err
		}
	}
	for _, pair := range [][2]string{{"Service", serviceName(s)}, {"NetworkPolicy", policyName(s)}} {
		if err := m.Client.Delete(ctx, pair[0], pair[1]); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	for {
		allGone := true
		for _, pair := range [][2]string{{"Service", serviceName(s)}, {"NetworkPolicy", policyName(s)}} {
			gone, err := m.gone(ctx, pair[0], pair[1])
			if err != nil {
				return err
			}
			allGone = allGone && gone
		}
		if allGone {
			return nil
		}
		if err := wait(ctx, m.poll()); err != nil {
			return err
		}
	}
}

func (m Manager) gone(ctx context.Context, kind, name string) (bool, error) {
	state, err := m.Client.Observe(ctx, kind, name)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return !state.Exists, nil
}

// Run always performs cleanup, including when the caller cancels. Reports must
// be uploaded by the caller before task PVC retention proceeds.
func (m Manager) Run(ctx context.Context, s Spec) (results []Result, err error) {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := m.Stop(cleanup, s); stopErr != nil {
			err = errors.Join(err, stopErr)
		}
	}()
	if err = m.Start(ctx, s); err != nil {
		return nil, err
	}
	return m.Verify(ctx, s)
}

// RunAndPersist is the production contract: the caller confirms all check
// reports are durably stored before ephemeral Pods and Jobs are removed. A
// persistence error preserves resources for diagnosis and retry. Cancellation
// and startup failure stop them with a fresh bounded cleanup context.
func (m Manager) RunAndPersist(ctx context.Context, s Spec, persist func(context.Context, []Result) error) (results []Result, err error) {
	if persist == nil {
		return nil, errors.New("report persistence callback required")
	}
	stop := func() error {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return m.Stop(cleanup, s)
	}
	if err = m.Start(ctx, s); err != nil {
		return nil, errors.Join(err, stop())
	}
	results, err = m.Verify(ctx, s)
	if err != nil {
		return results, errors.Join(err, stop())
	}
	if err = persist(ctx, results); err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return results, errors.Join(err, stop())
		}
		return results, err
	}
	return results, stop()
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func podName(s Spec) string          { return "sj-" + s.RunID + "-svc" }
func serviceName(s Spec) string      { return "sj-" + s.RunID + "-service" }
func policyName(s Spec) string       { return "sj-" + s.RunID + "-net" }
func jobName(s Spec, c Check) string { return "sj-" + s.RunID + "-" + c.Name }
func labels(s Spec, role string) map[string]any {
	return map[string]any{"app.kubernetes.io/name": "swarmjet-integration", "swarmjet.io/task-id": s.TaskID, "swarmjet.io/run-id": s.RunID, "swarmjet.io/role": role}
}
func resources(r Resources) map[string]any {
	return map[string]any{"requests": map[string]any{"cpu": r.CPURequest, "memory": r.MemoryRequest}, "limits": map[string]any{"cpu": r.CPULimit, "memory": r.MemoryLimit}}
}
func security() map[string]any {
	return map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}}
}
func podSecurity() map[string]any {
	return map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001, "seccompProfile": map[string]any{"type": "RuntimeDefault"}}
}
func mounts() []any {
	return []any{map[string]any{"name": "workspace", "mountPath": "/workspace", "readOnly": true}, map[string]any{"name": "tmp", "mountPath": "/tmp"}}
}
func volumes(s Spec) []any {
	return []any{map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": s.PVC}}, map[string]any{"name": "tmp", "emptyDir": map[string]any{"sizeLimit": "512Mi"}}}
}
func safePod(s Spec, role string, containers []any) map[string]any {
	return map[string]any{"metadata": map[string]any{"labels": labels(s, role)}, "spec": map[string]any{"restartPolicy": "Never", "serviceAccountName": "swarmjet-runner", "automountServiceAccountToken": false, "securityContext": podSecurity(), "containers": containers, "volumes": volumes(s)}}
}

// Build returns NetworkPolicy, Service, Pod in safe creation order. Check Jobs
// are created only after the service Pod becomes Ready.
func Build(s Spec) ([]Resource, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if len(podName(s)) > 63 || len(serviceName(s)) > 63 || len(policyName(s)) > 63 {
		return nil, errors.New("integration resource name too long")
	}
	var containers, servicePorts, ports, egressPorts []any
	for _, service := range s.Services {
		containers = append(containers, map[string]any{"name": service.Name, "image": service.Image, "imagePullPolicy": "IfNotPresent", "command": service.Command, "ports": []any{map[string]any{"name": service.Name, "containerPort": service.Port}}, "readinessProbe": map[string]any{"tcpSocket": map[string]any{"port": service.Port}, "periodSeconds": 1, "failureThreshold": 10}, "workingDir": "/workspace", "resources": resources(service.Resources), "securityContext": security(), "volumeMounts": mounts()})
		servicePorts = append(servicePorts, map[string]any{"name": service.Name, "port": service.Port, "targetPort": service.Port, "protocol": "TCP"})
		ports = append(ports, map[string]any{"protocol": "TCP", "port": service.Port})
		egressPorts = append(egressPorts, map[string]any{"protocol": "TCP", "port": service.Port})
	}
	selector := map[string]any{"matchLabels": map[string]any{"swarmjet.io/run-id": s.RunID, "swarmjet.io/role": "service"}}
	checks := map[string]any{"matchLabels": map[string]any{"swarmjet.io/run-id": s.RunID, "swarmjet.io/role": "check"}}
	policy := Resource{Kind: "NetworkPolicy", Name: policyName(s), Body: map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": policyName(s), "labels": labels(s, "policy")}, "spec": map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"swarmjet.io/run-id": s.RunID}}, "policyTypes": []string{"Ingress", "Egress"}, "ingress": []any{map[string]any{"from": []any{map[string]any{"podSelector": checks}}, "ports": ports}}, "egress": []any{map[string]any{"to": []any{map[string]any{"podSelector": selector}}, "ports": egressPorts}}}}}
	svc := Resource{Kind: "Service", Name: serviceName(s), Body: map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"name": serviceName(s), "labels": labels(s, "service")}, "spec": map[string]any{"selector": map[string]any{"swarmjet.io/run-id": s.RunID, "swarmjet.io/role": "service"}, "ports": servicePorts}}}
	pod := Resource{Kind: "Pod", Name: podName(s), Body: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": podName(s), "labels": labels(s, "service")}, "spec": safePod(s, "service", containers)["spec"]}}
	return []Resource{policy, svc, pod}, nil
}

func buildCheckJob(s Spec, c Check, podIP string) (Resource, error) {
	if err := s.Validate(); err != nil {
		return Resource{}, err
	}
	if len(jobName(s, c)) > 63 || podIP == "" {
		return Resource{}, errors.New("invalid check Job name or service IP")
	}
	container := map[string]any{"name": "check", "image": c.Image, "imagePullPolicy": "IfNotPresent", "command": c.Command, "workingDir": "/workspace", "env": []any{map[string]any{"name": "SWARMJET_SERVICE_HOST", "value": podIP}, map[string]any{"name": "SWARMJET_TASK_ID", "value": s.TaskID}}, "resources": resources(c.Resources), "securityContext": security(), "volumeMounts": mounts()}
	body := map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": jobName(s, c), "labels": labels(s, "check")}, "spec": map[string]any{"backoffLimit": 0, "parallelism": 1, "completions": 1, "activeDeadlineSeconds": c.TimeoutSeconds, "template": safePod(s, "check", []any{container})}}
	return Resource{Kind: "Job", Name: jobName(s, c), Body: body}, nil
}
