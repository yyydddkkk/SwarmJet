package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

func TestPatchHivePreservesConfigAndBootstrapsQueen(t *testing.T) {
	s, handler, cookie := newHiveAPITest(t)
	ctx := context.Background()
	h, err := s.CreateHive(ctx, domain.Hive{Repository: "acme/settings", Config: domain.HiveConfig{
		CLI: "omp", MaxHops: 17, MaxForagersPerChain: 23, IdleParkMinutes: 8,
		Triggers: []domain.TriggerRule{{Event: "issue_labeled", Label: "work", Preset: "worker"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/hives/" + h.ID
	body := map[string]any{"queenEnabled": true, "config": map[string]any{"queenModel": "provider/model", "maxConcurrentForagers": 7}}
	if w := hiveDo(t, handler, nil, http.MethodPatch, path, body); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}
	r := httptest.NewRequest(http.MethodPatch, "https://console.test"+path, strings.NewReader(`{}`))
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("csrf: %d", w.Code)
	}
	updated := hiveDecode[struct {
		Hive         domain.Hive `json:"hive"`
		LocalAddress string      `json:"localAddress"`
	}](t, hiveDo(t, handler, cookie, http.MethodPatch, path, body), http.StatusOK)
	want := h.Config
	want.QueenModel, want.MaxConcurrentForagers = "provider/model", 7
	if !updated.Hive.QueenEnabled || updated.Hive.Version != h.Version+1 || !reflect.DeepEqual(updated.Hive.Config, want) || updated.LocalAddress == "" {
		t.Fatalf("updated: %+v", updated)
	}
	// UpdateHive must reject a concurrent writer holding this old version.
	if _, err := s.UpdateHive(ctx, h); err != domain.ErrConflict {
		t.Fatalf("stale update: %v", err)
	}
	hiveDecode[map[string]any](t, hiveDo(t, handler, cookie, http.MethodPatch, path, map[string]any{"queenEnabled": true}), http.StatusOK)
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM messages WHERE hive_id=$1 AND id LIKE 'queen-bootstrap:%'`, h.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("bootstrap: %d %v", count, err)
	}
	for _, body := range []any{map[string]any{"config": nil}, map[string]any{"config": map[string]any{"unknown": 1}}, map[string]any{"config": "bad"}} {
		if w := hiveDo(t, handler, cookie, http.MethodPatch, path, body); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid body: %d %s", w.Code, w.Body.String())
		}
	}
	if w := hiveDo(t, handler, cookie, http.MethodPatch, path, map[string]any{"config": map[string]any{"maxConcurrentForagers": -1}}); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("negative limit: %d", w.Code)
	}
	if w := hiveDo(t, handler, cookie, http.MethodPatch, "/api/hives/missing", body); w.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", w.Code)
	}
}
