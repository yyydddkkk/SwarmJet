// Package k8srunner owns one Kubernetes Job per fenced execution attempt.
// The caller owns durable admission, lease versions, and business completion.
package k8srunner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const WorkspaceMount = "/workspace"

var dnsPart = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
var sha256Image = regexp.MustCompile(`^[^\s]+@sha256:[0-9a-f]{64}$`)

// Fence checks the durable execution right immediately before Job creation.
// A false result must prevent submission. The trusted platform must check the
// same right again before accepting results or performing remote writes.
type Fence func(context.Context, string, int64) (bool, error)

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type Controller struct {
	client    HTTPDoer
	base      *url.URL
	namespace string
	fence     Fence
}

func NewController(client HTTPDoer, apiBaseURL, namespace string, fence Fence) (*Controller, error) {
	if client == nil || fence == nil {
		return nil, errors.New("client and fence are required")
	}
	u, err := url.Parse(apiBaseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errors.New("invalid Kubernetes API URL")
	}
	if !validDNS(namespace) {
		return nil, errors.New("invalid namespace")
	}
	return &Controller{client: client, base: u, namespace: namespace, fence: fence}, nil
}

type Resources struct{ CPURequest, CPULimit, MemoryRequest, MemoryLimit string }

type Spec struct {
	ExecutionID string
	Generation  int64
	TaskID      string
	Image       string
	PVC         string
	Resources   Resources
	// Command is the executable and arguments inside the pinned image.
	Command []string
	// SecretName is optional; its keys become environment variables.
	SecretName string
}

func (s Spec) Validate() error {
	if !validDNS(s.ExecutionID) || len(s.ExecutionID) > 40 {
		return errors.New("invalid execution ID")
	}
	if s.Generation < 1 {
		return errors.New("generation must be positive")
	}
	if !validDNS(s.TaskID) {
		return errors.New("invalid task ID")
	}
	if !sha256Image.MatchString(s.Image) {
		return errors.New("image must use a sha256 digest")
	}
	if !validDNS(s.PVC) {
		return errors.New("invalid PVC name")
	}
	if s.SecretName != "" && !validDNS(s.SecretName) {
		return errors.New("invalid Secret name")
	}
	if s.Resources.CPURequest == "" || s.Resources.CPULimit == "" || s.Resources.MemoryRequest == "" || s.Resources.MemoryLimit == "" {
		return errors.New("resource requests and limits are required")
	}
	if len(s.Command) == 0 || s.Command[0] == "" {
		return errors.New("runner command is required")
	}
	for _, arg := range s.Command {
		if strings.ContainsRune(arg, '\x00') {
			return errors.New("invalid runner command")
		}
	}
	return nil
}

func validDNS(s string) bool { return len(s) > 0 && len(s) <= 63 && dnsPart.MatchString(s) }

func JobName(executionID string, generation int64) string {
	return "sj-" + executionID + "-g" + strconv.FormatInt(generation, 10)
}

// Job returns the exact submitted resource for inspection and integration tests.
type Job map[string]any

func BuildJob(s Spec) (Job, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	name := JobName(s.ExecutionID, s.Generation)
	if len(name) > 63 {
		return nil, errors.New("Job name exceeds DNS label limit")
	}
	labels := map[string]any{"app.kubernetes.io/name": "swarmjet-runner", "swarmjet.io/execution-id": s.ExecutionID, "swarmjet.io/generation": strconv.FormatInt(s.Generation, 10)}
	env := []any{map[string]any{"name": "SWARMJET_EXECUTION_ID", "value": s.ExecutionID}, map[string]any{"name": "SWARMJET_GENERATION", "value": strconv.FormatInt(s.Generation, 10)}, map[string]any{"name": "SWARMJET_TASK_ID", "value": s.TaskID}}
	container := map[string]any{
		"name": "runner", "image": s.Image, "imagePullPolicy": "IfNotPresent", "command": s.Command,
		"workingDir": WorkspaceMount, "env": env,
		"resources":       map[string]any{"requests": map[string]any{"cpu": s.Resources.CPURequest, "memory": s.Resources.MemoryRequest}, "limits": map[string]any{"cpu": s.Resources.CPULimit, "memory": s.Resources.MemoryLimit}},
		"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}},
		"volumeMounts":    []any{map[string]any{"name": "workspace", "mountPath": WorkspaceMount}, map[string]any{"name": "tmp", "mountPath": "/tmp"}},
	}
	if s.SecretName != "" {
		container["envFrom"] = []any{map[string]any{"secretRef": map[string]any{"name": s.SecretName}}}
	}
	contract, _ := json.Marshal(map[string]any{"spec": s, "runnerSecurity": container["securityContext"]})
	hash := sha256.Sum256(contract)
	annotations := map[string]any{"swarmjet.io/contract-sha256": hex.EncodeToString(hash[:])}
	return Job{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"name": name, "labels": labels, "annotations": annotations}, "spec": map[string]any{
		"backoffLimit": 0, "parallelism": 1, "completions": 1,
		"template": map[string]any{"metadata": map[string]any{"labels": labels, "annotations": annotations}, "spec": map[string]any{
			"restartPolicy": "Never", "serviceAccountName": "swarmjet-runner", "automountServiceAccountToken": false,
			"securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001, "seccompProfile": map[string]any{"type": "RuntimeDefault"}},
			"containers":      []any{container}, "volumes": []any{map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": s.PVC}}, map[string]any{"name": "tmp", "emptyDir": map[string]any{"sizeLimit": "1Gi"}}},
		}},
	}}, nil
}

func (c *Controller) path(name string) string {
	p := "/apis/batch/v1/namespaces/" + c.namespace + "/jobs"
	if name != "" {
		p += "/" + name
	}
	return p
}

func (c *Controller) request(ctx context.Context, method, path string, body any, out any) (int, error) {
	u := *c.base
	requestPath, query, _ := strings.Cut(path, "?")
	u.Path = strings.TrimRight(c.base.Path, "/") + requestPath
	u.RawQuery = query
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("Kubernetes API %s %s: HTTP %d: %s", method, path, resp.StatusCode, string(data))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func (c *Controller) Create(ctx context.Context, s Spec) (Job, error) {
	job, err := BuildJob(s)
	if err != nil {
		return nil, err
	}
	ok, err := c.fence(ctx, s.ExecutionID, s.Generation)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("execution lease is no longer current")
	}
	var created Job
	status, err := c.request(ctx, http.MethodPost, c.path(""), job, &created)
	if status == http.StatusConflict {
		existing, getErr := c.ObserveJob(ctx, s.ExecutionID, s.Generation)
		if getErr != nil {
			return nil, getErr
		}
		if err := verifyExisting(existing, job); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if err != nil {
		return nil, err
	}
	return created, nil
}

// A retry may encounter a Job from a response lost after creation. Refuse a
// name collision unless the immutable workload contract is the same.
func verifyExisting(existing, desired Job) error {
	var a, b struct {
		Metadata struct {
			Name        string
			Labels      map[string]string
			Annotations map[string]string
		}
		Spec struct {
			BackoffLimit, Parallelism, Completions int
			Template                               struct {
				Spec struct {
					RestartPolicy, ServiceAccountName string
					AutomountServiceAccountToken      bool
					Containers                        []struct {
						Image     string
						Command   []string
						Resources map[string]map[string]string
					}
					Volumes []struct{ PersistentVolumeClaim *struct{ ClaimName string } }
				}
			}
		}
	}
	ad, _ := json.Marshal(existing)
	bd, _ := json.Marshal(desired)
	if json.Unmarshal(ad, &a) != nil || json.Unmarshal(bd, &b) != nil {
		return errors.New("invalid existing Job")
	}
	if !reflect.DeepEqual(a, b) {
		return errors.New("existing Job conflicts with requested execution attempt")
	}
	return nil
}

func (c *Controller) ObserveJob(ctx context.Context, executionID string, generation int64) (Job, error) {
	if !validDNS(executionID) || generation < 1 {
		return nil, errors.New("invalid attempt")
	}
	var job Job
	_, err := c.request(ctx, http.MethodGet, c.path(JobName(executionID, generation)), nil, &job)
	return job, err
}

type Observation struct {
	Exists bool
	// Stopped means the API reports no Job or Pods; it is not node fencing.
	Stopped                   bool
	Active, Succeeded, Failed int
	Complete, Failure         bool
	Reason                    string
	Pods                      []PodObservation
}

type PodObservation struct {
	Name        string
	UID         string
	Phase       string
	Terminating bool
}

func (c *Controller) ObservePods(ctx context.Context, executionID string, generation int64) ([]PodObservation, error) {
	if !validDNS(executionID) || generation < 1 {
		return nil, errors.New("invalid attempt")
	}
	u := "/api/v1/namespaces/" + c.namespace + "/pods?labelSelector=" + url.QueryEscape("batch.kubernetes.io/job-name="+JobName(executionID, generation))
	var list struct {
		Items []struct {
			Metadata struct{ Name, UID, DeletionTimestamp string }
			Status   struct{ Phase string }
		}
	}
	_, err := c.request(ctx, http.MethodGet, u, nil, &list)
	if err != nil {
		return nil, err
	}
	pods := make([]PodObservation, 0, len(list.Items))
	for _, p := range list.Items {
		pods = append(pods, PodObservation{Name: p.Metadata.Name, UID: p.Metadata.UID, Phase: p.Status.Phase, Terminating: p.Metadata.DeletionTimestamp != ""})
	}
	return pods, nil
}

func (c *Controller) Observe(ctx context.Context, executionID string, generation int64) (Observation, error) {
	job, err := c.ObserveJob(ctx, executionID, generation)
	if err != nil {
		if strings.Contains(err.Error(), "HTTP 404:") {
			pods, podErr := c.ObservePods(ctx, executionID, generation)
			if podErr != nil {
				return Observation{}, podErr
			}
			return Observation{Pods: pods, Stopped: len(pods) == 0}, nil
		}
		return Observation{}, err
	}
	var state struct {
		Status struct {
			Active, Succeeded, Failed int
			Conditions                []struct{ Type, Status, Reason string }
		}
	}
	data, _ := json.Marshal(job)
	if err := json.Unmarshal(data, &state); err != nil {
		return Observation{}, err
	}
	o := Observation{Exists: true, Active: state.Status.Active, Succeeded: state.Status.Succeeded, Failed: state.Status.Failed}
	for _, c := range state.Status.Conditions {
		if c.Status == "True" {
			if c.Type == "Complete" {
				o.Complete = true
			}
			if c.Type == "Failed" {
				o.Failure = true
			}
			o.Reason = c.Reason
		}
	}
	o.Pods, err = c.ObservePods(ctx, executionID, generation)
	if err != nil {
		return Observation{}, err
	}
	o.Stopped = false
	return o, nil
}

// Cancel deletes only the named generation and waits until both Job and Pods
// disappear. Node partition or force deletion still needs storage/node fencing
// before another PVC writer may start.
func (c *Controller) Cancel(ctx context.Context, executionID string, generation int64) error {
	if !validDNS(executionID) || generation < 1 {
		return errors.New("invalid attempt")
	}
	body := map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "propagationPolicy": "Foreground"}
	status, err := c.request(ctx, http.MethodDelete, c.path(JobName(executionID, generation)), body, nil)
	if status != http.StatusNotFound && err != nil {
		return err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, jobErr := c.ObserveJob(ctx, executionID, generation)
		if jobErr != nil && !strings.Contains(jobErr.Error(), "HTTP 404:") {
			return jobErr
		}
		pods, podErr := c.ObservePods(ctx, executionID, generation)
		if podErr != nil {
			return podErr
		}
		if (job == nil || jobErr != nil) && len(pods) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
