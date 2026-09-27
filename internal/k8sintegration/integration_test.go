package k8sintegration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

const image = "example.invalid/test@sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func fixture(multi bool) Spec {
	r := Resources{CPURequest: "50m", CPULimit: "200m", MemoryRequest: "64Mi", MemoryLimit: "256Mi"}
	s := Spec{TaskID: "task", RunID: "verify", PVC: "task-pvc", StartupTimeoutSeconds: 30, Services: []Container{{Name: "api", Image: image, Command: []string{"/api"}, Port: 8080, Resources: r}}, Checks: []Check{{Name: "http", Image: image, Command: []string{"/check", "$(SWARMJET_SERVICE_HOST)"}, Resources: r, TimeoutSeconds: 30}}}
	if multi {
		s.Services = append(s.Services, Container{Name: "database", Image: image, Command: []string{"/database"}, Port: 5432, Resources: r})
	}
	return s
}

func manifestMap(t *testing.T, resource Resource) map[string]any {
	t.Helper()
	b, err := json.Marshal(resource.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err = json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func field(t *testing.T, m map[string]any, names ...string) any {
	t.Helper()
	var v any = m
	for _, name := range names {
		next, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%v is not a map", names)
		}
		v = next[name]
	}
	return v
}

func TestBuildSingleAndMultipleContainersRestricted(t *testing.T) {
	for _, multi := range []bool{false, true} {
		s := fixture(multi)
		resources, err := Build(s)
		if err != nil {
			t.Fatal(err)
		}
		if len(resources) != 3 || resources[0].Kind != "NetworkPolicy" || resources[1].Kind != "Service" || resources[2].Kind != "Pod" {
			t.Fatal("unsafe creation order")
		}
		pod := manifestMap(t, resources[2])
		spec := field(t, pod, "spec").(map[string]any)
		if spec["automountServiceAccountToken"] != false || spec["serviceAccountName"] != "swarmjet-runner" {
			t.Fatal("Pod has API identity")
		}
		containers := spec["containers"].([]any)
		expected := 1
		if multi {
			expected = 2
		}
		if len(containers) != expected {
			t.Fatalf("containers=%d", len(containers))
		}
		for _, raw := range containers {
			c := raw.(map[string]any)
			if !strings.Contains(c["image"].(string), "@sha256:") {
				t.Fatal("unfixed image")
			}
			security := c["securityContext"].(map[string]any)
			if security["allowPrivilegeEscalation"] != false || security["readOnlyRootFilesystem"] != true {
				t.Fatal("weak container security")
			}
			mounts := c["volumeMounts"].([]any)
			if mounts[0].(map[string]any)["readOnly"] != true {
				t.Fatal("workspace writable")
			}
		}
		policy := manifestMap(t, resources[0])
		types := field(t, policy, "spec", "policyTypes").([]any)
		if len(types) != 2 {
			t.Fatal("network policy not ingress and egress restricted")
		}
		job, err := buildCheckJob(s, s.Checks[0], "10.1.2.3")
		if err != nil {
			t.Fatal(err)
		}
		body := manifestMap(t, job)
		if field(t, body, "spec", "backoffLimit") != float64(0) {
			t.Fatal("Job retries enabled")
		}
		if field(t, body, "spec", "activeDeadlineSeconds") != float64(30) {
			t.Fatal("Job timeout missing")
		}
	}
}

type fakeClient struct {
	mu        sync.Mutex
	resources map[string]Resource
	outcomes  map[string]string
	onCreate  func(Resource)
}

func key(kind, name string) string { return kind + "/" + name }
func (f *fakeClient) Create(_ context.Context, r Resource) error {
	f.mu.Lock()
	if f.resources == nil {
		f.resources = map[string]Resource{}
	}
	f.resources[key(r.Kind, r.Name)] = r
	callback := f.onCreate
	f.mu.Unlock()
	if callback != nil {
		callback(r)
	}
	return nil
}
func (f *fakeClient) Observe(_ context.Context, kind, name string) (Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.resources[key(kind, name)]; !ok {
		return Status{}, ErrNotFound
	}
	s := Status{Exists: true}
	if kind == "Pod" {
		s.Ready = true
		s.PodIP = "10.1.2.3"
	}
	if kind == "Job" {
		switch f.outcomes[name] {
		case "success":
			s.Complete = true
		case "failure":
			s.Failed = true
			s.Reason = "exit 1"
		}
	}
	return s, nil
}
func (f *fakeClient) JobLog(_ context.Context, name string) ([]byte, error) {
	return []byte("report for " + name), nil
}
func (f *fakeClient) Delete(_ context.Context, kind, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.resources[key(kind, name)]; !ok {
		return ErrNotFound
	}
	delete(f.resources, key(kind, name))
	return nil
}
func (f *fakeClient) empty() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.resources) == 0 }

func TestRunSuccessFailureAndCleanup(t *testing.T) {
	for _, outcome := range []string{"success", "failure"} {
		s := fixture(outcome == "failure")
		f := &fakeClient{outcomes: map[string]string{jobName(s, s.Checks[0]): outcome}}
		m := Manager{Client: f, PollInterval: time.Millisecond}
		results, err := m.Run(context.Background(), s)
		if err != nil || len(results) != 1 || results[0].Passed != (outcome == "success") || len(results[0].Report) == 0 {
			t.Fatalf("%s: %+v %v", outcome, results, err)
		}
		if !f.empty() {
			t.Fatal("resources retained after verification")
		}
		if err = m.Stop(context.Background(), s); err != nil {
			t.Fatal("idempotent cleanup", err)
		}
	}
}

func TestRunCancellationStopsResources(t *testing.T) {
	s := fixture(true)
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeClient{onCreate: func(r Resource) {
		if r.Kind == "Job" {
			cancel()
		}
	}}
	m := Manager{Client: f, PollInterval: time.Millisecond}
	_, err := m.Run(ctx, s)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result: %v", err)
	}
	if !f.empty() {
		t.Fatal("cancel left resources running")
	}
}

func TestRunAndPersistKeepsReportOnSaveFailure(t *testing.T) {
	s := fixture(false)
	f := &fakeClient{outcomes: map[string]string{jobName(s, s.Checks[0]): "success"}}
	m := Manager{Client: f, PollInterval: time.Millisecond}
	want := errors.New("object store unavailable")
	_, err := m.RunAndPersist(context.Background(), s, func(_ context.Context, results []Result) error {
		if len(results) != 1 || len(results[0].Report) == 0 || f.empty() {
			t.Fatal("resources removed before report save")
		}
		return want
	})
	if !errors.Is(err, want) || f.empty() {
		t.Fatalf("report save failure lost resources: %v", err)
	}
	if err = m.Stop(context.Background(), s); err != nil || !f.empty() {
		t.Fatalf("manual cleanup: %v", err)
	}
}

func TestRunAndPersistCancellationDuringSaveStopsResources(t *testing.T) {
	s := fixture(false)
	f := &fakeClient{outcomes: map[string]string{jobName(s, s.Checks[0]): "success"}}
	m := Manager{Client: f, PollInterval: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := m.RunAndPersist(ctx, s, func(context.Context, []Result) error { cancel(); return context.Canceled })
	if !errors.Is(err, context.Canceled) || !f.empty() {
		t.Fatalf("cancel during save left resources: %v", err)
	}
}

func TestRejectsUnpinnedImageAndDuplicatePort(t *testing.T) {
	s := fixture(true)
	s.Services[1].Port = s.Services[0].Port
	if _, err := Build(s); err == nil {
		t.Fatal("duplicate port accepted")
	}
	s = fixture(false)
	s.Services[0].Image = "example:latest"
	if _, err := Build(s); err == nil {
		t.Fatal("mutable image accepted")
	}
}
