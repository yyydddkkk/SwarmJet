package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

func publishAPIMethodology(t *testing.T, s *store.Store, hiveID, note string) domain.Methodology {
	t.Helper()
	manifest, files := methodology.Builtin()
	manifest.StandingOrders = []string{"order for " + note}
	published, err := s.PublishMethodology(context.Background(), domain.Methodology{
		HiveID:    hiveID,
		Manifest:  manifest,
		Note:      note,
		CreatedBy: domain.LocalAddress("web-admin"),
	}, files)
	if err != nil {
		t.Fatalf("publish %s: %v", note, err)
	}
	return published
}

func TestMethodologyAPIReadsVersionsAndActivates(t *testing.T) {
	s, handler, cookie := newHiveAPITest(t)
	hive := createHiveForTest(t, handler, cookie, createHiveRequest{Name: "Methodology", Repository: "acme/methodology"})
	publishAPIMethodology(t, s, hive.ID, "first")
	publishAPIMethodology(t, s, hive.ID, "second")

	active := hiveDecode[methodologyResponse](t, hiveDo(t, handler, cookie, http.MethodGet,
		"/api/hives/"+hive.ID+"/methodology", nil), http.StatusOK)
	if active.Version != 2 || !active.Active || active.Builtin || active.Note != "second" {
		t.Fatalf("active methodology = %+v", active)
	}
	if len(active.Files) != 0 {
		t.Fatalf("builtin files = %+v", active.Files)
	}
	if len(active.Manifest.Roles) != 1 || active.Manifest.DefaultRole != "worker" {
		t.Fatalf("manifest = %+v", active.Manifest)
	}

	first := hiveDecode[methodologyResponse](t, hiveDo(t, handler, cookie, http.MethodGet,
		"/api/hives/"+hive.ID+"/methodology?version=1", nil), http.StatusOK)
	if first.Version != 1 || first.Note != "first" || first.Active {
		t.Fatalf("version 1 = %+v", first)
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet,
		"/api/hives/"+hive.ID+"/methodology?version=99", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing version status = %d", w.Code)
	}

	history := hiveDecode[struct {
		HiveID        string               `json:"hiveId"`
		ActiveVersion int                  `json:"activeVersion"`
		Versions      []domain.Methodology `json:"versions"`
		Changes       []methodology.Change `json:"changes"`
	}](t, hiveDo(t, handler, cookie, http.MethodGet,
		"/api/hives/"+hive.ID+"/methodology/versions", nil), http.StatusOK)
	if history.ActiveVersion != 2 || len(history.Versions) != 2 || len(history.Changes) != 2 {
		t.Fatalf("history = %+v", history)
	}

	activated := hiveDecode[methodologyResponse](t, hiveDo(t, handler, cookie, http.MethodPost,
		"/api/hives/"+hive.ID+"/methodology/activate", map[string]any{"version": 1, "note": "rollback"}), http.StatusOK)
	if activated.Version != 1 || !activated.Active {
		t.Fatalf("activated = %+v", activated)
	}
	active = hiveDecode[methodologyResponse](t, hiveDo(t, handler, cookie, http.MethodGet,
		"/api/hives/"+hive.ID+"/methodology", nil), http.StatusOK)
	if active.Version != 1 {
		t.Fatalf("active after activation = %+v", active)
	}
	changes, err := s.MethodologyChanges(context.Background(), hive.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 || changes[2].Action != methodology.ActionActivate || changes[2].Note != "rollback" {
		t.Fatalf("changes = %+v", changes)
	}
	inbox, err := s.Inbox(context.Background(), hive.ID, domain.AddressHive, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].From != domain.AddressSystem || !strings.Contains(inbox[0].Body, "methodology activate") {
		t.Fatalf("system messages = %+v", inbox)
	}

	if w := hiveDo(t, handler, cookie, http.MethodPost,
		"/api/hives/"+hive.ID+"/methodology/activate", map[string]any{"version": 99}); w.Code != http.StatusNotFound {
		t.Fatalf("activate missing version status = %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodPost,
		"/api/hives/"+hive.ID+"/methodology/activate", map[string]any{"version": 0}); w.Code != http.StatusBadRequest {
		t.Fatalf("activate zero version status = %d", w.Code)
	}
}

func TestMethodologyAPIFallsBackToBuiltinAndValidatesRoutes(t *testing.T) {
	_, handler, cookie := newHiveAPITest(t)
	hive := createHiveForTest(t, handler, cookie, createHiveRequest{Name: "Empty", Repository: "acme/empty"})

	builtin := hiveDecode[methodologyResponse](t, hiveDo(t, handler, cookie, http.MethodGet,
		"/api/hives/"+hive.ID+"/methodology", nil), http.StatusOK)
	if !builtin.Builtin || builtin.Version != 0 || builtin.Manifest.DefaultRole != "worker" {
		t.Fatalf("builtin = %+v", builtin)
	}
	history := hiveDecode[struct {
		Versions []domain.Methodology `json:"versions"`
		Changes  []methodology.Change `json:"changes"`
	}](t, hiveDo(t, handler, cookie, http.MethodGet,
		"/api/hives/"+hive.ID+"/methodology/versions", nil), http.StatusOK)
	if len(history.Versions) != 0 || len(history.Changes) != 0 {
		t.Fatalf("empty history = %+v", history)
	}

	if w := hiveDo(t, handler, cookie, http.MethodGet,
		"/api/hives/"+hive.ID+"/methodology/nope", nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown subroute status = %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodPost,
		"/api/hives/"+hive.ID+"/methodology", map[string]any{}); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST methodology status = %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodGet,
		"/api/hives/missing/methodology", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing hive status = %d", w.Code)
	}
}
