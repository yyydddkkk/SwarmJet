package k8srunner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sampleSpec() Spec {
	return Spec{ExecutionID: "exec-a", Generation: 2, TaskID: "task-a", Image: "example/runner@sha256:" + strings.Repeat("a", 64), PVC: "task-workspace", Resources: Resources{"100m", "500m", "128Mi", "512Mi"}, Command: []string{"/runner"}}
}

func TestBuildJobSecurityContract(t *testing.T) {
	job, err := BuildJob(sampleSpec())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(job)
	var v struct {
		Spec struct {
			BackoffLimit int
			Template     struct {
				Spec struct {
					AutomountServiceAccountToken bool
					RestartPolicy                string
					ServiceAccountName           string
					Containers                   []struct {
						Image           string
						SecurityContext struct {
							AllowPrivilegeEscalation bool
							ReadOnlyRootFilesystem   bool
							RunAsNonRoot             bool
							Capabilities             struct{ Drop []string }
						}
						Resources struct{ Requests, Limits map[string]string }
					}
					Volumes []struct{ PersistentVolumeClaim *struct{ ClaimName string } }
				}
			}
		}
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	p := v.Spec.Template.Spec
	if v.Spec.BackoffLimit != 0 || p.AutomountServiceAccountToken || p.RestartPolicy != "Never" || p.ServiceAccountName != "swarmjet-runner" {
		t.Fatalf("unsafe pod/job policy: %+v", v)
	}
	if len(p.Containers) != 1 || !p.Containers[0].SecurityContext.RunAsNonRoot || !p.Containers[0].SecurityContext.ReadOnlyRootFilesystem || p.Containers[0].SecurityContext.AllowPrivilegeEscalation || len(p.Containers[0].SecurityContext.Capabilities.Drop) != 1 || p.Containers[0].SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatalf("unsafe container: %+v", p.Containers)
	}
	if len(p.Containers[0].Resources.Requests) != 2 || len(p.Containers[0].Resources.Limits) != 2 || len(p.Volumes) != 2 || p.Volumes[0].PersistentVolumeClaim.ClaimName != "task-workspace" {
		t.Fatalf("missing resource or PVC contract: %+v", p)
	}
}

func TestCreateFencedAndConflictChecked(t *testing.T) {
	spec := sampleSpec()
	want, _ := BuildJob(spec)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method == "POST" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()
	c, _ := NewController(srv.Client(), srv.URL, "runners", StaticFence(false))
	if _, err := c.Create(context.Background(), spec); err == nil || calls.Load() != 0 {
		t.Fatal("rejected lease submitted a Job")
	}
	c, _ = NewController(srv.Client(), srv.URL, "runners", StaticFence(true))
	if _, err := c.Create(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	bad := sampleSpec()
	bad.Image = "example/runner@sha256:" + strings.Repeat("b", 64)
	if _, err := c.Create(context.Background(), bad); err == nil {
		t.Fatal("accepted mismatched existing Job")
	}
}

func TestCancelWaitsForPodAbsence(t *testing.T) {
	var pods atomic.Int32
	pods.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.Contains(r.URL.Path, "/pods") {
			if r.URL.Query().Get("labelSelector") != "batch.kubernetes.io/job-name=sj-exec-a-g2" {
				t.Errorf("bad selector: %s", r.URL.RawQuery)
			}
			if pods.Load() > 0 {
				w.Write([]byte(`{"items":[{"metadata":{"name":"pod-a","uid":"uid-a"},"status":{"phase":"Running"}}]}`))
			} else {
				w.Write([]byte(`{"items":[]}`))
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c, _ := NewController(srv.Client(), srv.URL, "runners", StaticFence(true))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.Cancel(ctx, "exec-a", 2); err == nil {
		t.Fatal("returned while Pod still existed")
	}
	pods.Store(0)
	if err := c.Cancel(context.Background(), "exec-a", 2); err != nil {
		t.Fatal(err)
	}
}
