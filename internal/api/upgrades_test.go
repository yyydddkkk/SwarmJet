package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/store"
)

func TestUpgradePlanSurvivesBuildAndControlSwitch(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	root, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	id, _ := newID()
	schema := "api_upgrade_" + strings.ReplaceAll(id, "-", "")
	if _, err = root.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer root.Pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	config := root.Pool.Config()
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := &store.Store{Pool: pool}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	reference := "registry.example/swarmjet/platform@sha256:" + strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("a", 64)
	builder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-SwarmJet-Runtime-Secret") != "builder-secret" {
			t.Errorf("builder authentication missing")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/images/platform/releases/builds":
			_, _ = w.Write([]byte(`{"id":"build-1","status":"building"}`))
		case "/images/platform":
			_, _ = w.Write([]byte(`{"builds":[{"id":"build-1","status":"succeeded","release":{"version":"v1.2.3","commitSHA":"` + strings.Repeat("b", 40) + `","platform":"linux/amd64","reference":"` + reference + `","digest":"` + digest + `"}}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer builder.Close()
	updater := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && r.Header.Get("Authorization") != "Bearer updater-secret" {
			t.Errorf("updater authentication missing")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/healthz", "/v1/preflight":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/v1/plan":
			_, _ = w.Write([]byte(`{"operationId":"` + strings.Repeat("c", 32) + `","status":"planned","backupDir":"/backup"}`))
		case "/v1/apply":
			_, _ = w.Write([]byte(`{"status":"accepted"}`))
		case "/v1/status":
			_, _ = w.Write([]byte(`{"status":"complete"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer updater.Close()
	a := &server{s: s, cfg: Config{BuilderURL: builder.URL, RuntimeSecret: "builder-secret", UpdaterURL: updater.URL, UpdaterSecret: "updater-secret", PlatformImage: "old:v1"}}
	request := func(method, path, body string) (int, upgradePlan) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		a.upgrades(w, r)
		var plan upgradePlan
		_ = json.Unmarshal(w.Body.Bytes(), &plan)
		return w.Code, plan
	}
	code, plan := request("POST", "/api/system/upgrades/prepare", `{"tag":"v1.2.3"}`)
	if code != 202 || plan.BuildID != "build-1" {
		t.Fatalf("prepare=%d %+v", code, plan)
	}
	a.advanceActiveUpgrade(ctx)
	code, plan = request("GET", "/api/system/upgrades/"+plan.ID, "")
	if code != 200 || plan.Status != "ready" || plan.Target.Reference != reference {
		t.Fatalf("ready=%d %+v", code, plan)
	}
	code, plan = request("POST", "/api/system/upgrades/"+plan.ID+"/start", `{}`)
	if code != 202 || plan.Stage != "draining" || plan.StartedAt == nil {
		t.Fatalf("start=%d %+v", code, plan)
	}
	for range 3 {
		a.advanceActiveUpgrade(ctx)
	}
	code, plan = request("GET", "/api/system/upgrades/"+plan.ID, "")
	if code != 200 || plan.Status != "succeeded" || plan.BackupDir != "/backup" {
		t.Fatalf("completed=%d %+v", code, plan)
	}
	maintenance, err := s.UpgradeMaintenance(ctx)
	if err != nil || maintenance {
		t.Fatalf("maintenance=%v err=%v", maintenance, err)
	}
	second := upgradePlan{ID: "abort-before-switch", Tag: "v1.2.4", Status: "ready", Stage: "ready", Current: upgradeTarget{Reference: "old:v1"}, Target: upgradeTarget{Tag: "v1.2.4", Reference: reference}}
	if err := s.CreateUpgrade(ctx, second.ID, second.Status, second); err != nil {
		t.Fatal(err)
	}
	code, second = request("POST", "/api/system/upgrades/"+second.ID+"/start", `{}`)
	if code != 202 || second.Stage != "draining" {
		t.Fatalf("second start=%d %+v", code, second)
	}
	code, second = request("POST", "/api/system/upgrades/"+second.ID+"/abort", `{}`)
	if code != 200 || second.Status != "cancelled" {
		t.Fatalf("abort=%d %+v", code, second)
	}
	maintenance, err = s.UpgradeMaintenance(ctx)
	if err != nil || maintenance {
		t.Fatalf("maintenance after abort=%v err=%v", maintenance, err)
	}
	if err = s.HeartbeatNode(ctx, domain.Node{ID: "legacy-node", Address: "http://legacy:8092", Capacity: 1}); err != nil {
		t.Fatal(err)
	}
	legacyPlan := upgradePlan{Tag: "v1.2.4", Current: upgradeTarget{Reference: "old:v1"}, Target: upgradeTarget{Tag: "v1.2.4", Reference: reference}}
	checks, ok := a.upgradePreflight(ctx, &legacyPlan)
	if ok || len(legacyPlan.Nodes) != 1 {
		t.Fatalf("legacy node not blocked: checks=%+v nodes=%+v", checks, legacyPlan.Nodes)
	}
}

func TestUpgradeRequiresNewerStableRelease(t *testing.T) {
	if !newerRelease("v1.2.4", "v1.2.3") || !newerRelease("v2.0.0", "v1.99.99") || !newerRelease("v1.0.0", "dev") {
		t.Fatal("valid upgrade rejected")
	}
	for _, tag := range []string{"v1.2.3", "v1.2.2", "v0.99.99"} {
		if newerRelease(tag, "v1.2.3") {
			t.Fatalf("downgrade accepted: %s", tag)
		}
	}
}
