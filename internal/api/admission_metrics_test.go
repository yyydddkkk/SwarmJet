package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarmjet/internal/domain"
	"swarmjet/internal/observability"
)

func TestManualSchedulingCountsOnlyImmediateAdmission(t *testing.T) {
	metrics := observability.New()
	a := &server{cfg: Config{Metrics: metrics}}
	a.recordScheduledTask(domain.Task{ID: "waiting-task", AdmissionBackend: "node", AdmissionStatus: "waiting"})
	a.recordScheduledTask(domain.Task{ID: "admitted-task", AdmissionBackend: "local", AdmissionStatus: "assigned"})
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	if !strings.Contains(body, `swarmjet_admitted_tasks_total{backend="local_or_node"} 1`) || strings.Contains(body, "admitted-task") {
		t.Fatalf("wrong manual admission metric: %s", body)
	}
}
