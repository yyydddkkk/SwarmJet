package registryconfig

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"swarmjet/internal/store"
)

func TestBuiltinDefaultAndCustomOverride(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	id := make([]byte, 8)
	if _, err = rand.Read(id); err != nil {
		t.Fatal(err)
	}
	schema := "registry_test_" + hex.EncodeToString(id)
	if _, err = admin.Pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	config := admin.Pool.Config()
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
	t.Setenv("SWARMJET_BUILTIN_REGISTRY_ADDRESS", "10.0.0.20:5443")
	t.Setenv("REGISTRY_PUSH_USER", "builder")
	t.Setenv("REGISTRY_PUSH_PASSWORD", "push-secret")
	t.Setenv("REGISTRY_PULL_USER", "reader")
	t.Setenv("REGISTRY_PULL_PASSWORD", "pull-secret")
	builtin, access, err := Load(ctx, s, nil, true)
	if err != nil || builtin.Mode != "builtin" || builtin.Address != "10.0.0.20:5443" || access.Username != "builder" || access.Password != "push-secret" {
		t.Fatalf("default: %+v %+v %v", builtin, access, err)
	}
	custom := Config{Mode: "custom", Address: "registry.example:5443", Repository: "team/agent", PushUser: "builder", PullUser: "reader"}
	if err = s.SetSetting(ctx, "registry", Saved{Config: custom}); err != nil {
		t.Fatal(err)
	}
	chosen, access, err := Load(ctx, s, nil, false)
	if err != nil || chosen.Mode != "custom" || chosen.Address != custom.Address || !chosen.BuiltinAvailable || access.Username != "reader" {
		t.Fatalf("custom: %+v %+v %v", chosen, access, err)
	}
	if err = s.SetSetting(ctx, "registry", Saved{Config: Config{Mode: "builtin"}}); err != nil {
		t.Fatal(err)
	}
	restored, _, err := Load(ctx, s, nil, false)
	if err != nil || restored.Mode != "builtin" || restored.Address != builtin.Address {
		t.Fatalf("restored: %+v %v", restored, err)
	}
}
