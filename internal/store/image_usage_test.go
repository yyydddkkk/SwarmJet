package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestImageDeletionGuardProtectsLiveReferences(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ref := "registry.test/swarmjet/agent@sha256:" + strings.Repeat("a", 64)
	if err := s.SetSetting(ctx, "registry", map[string]any{"config": map[string]string{"address": "registry.test", "repository": "swarmjet/agent"}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO nodes(id,address,capacity,last_seen) VALUES('node-a','http://node',1,now())`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO node_configs(id,version,body) VALUES('node-a',1,$1)`, `{"image":"`+ref+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	usage, err := s.WithImageDeletionGuard(ctx, ref, func() error { called = true; return nil })
	if !errors.Is(err, ErrConflict) || called || len(usage) != 1 || usage[0] != "节点 node-a" {
		t.Fatalf("usage=%v called=%v err=%v", usage, called, err)
	}
	_, err = s.Pool.Exec(ctx, `UPDATE node_configs SET body='{"image":""}' WHERE id='node-a'`)
	if err != nil {
		t.Fatal(err)
	}
	usage, err = s.WithImageDeletionGuard(ctx, ref, func() error { called = true; return nil })
	if err != nil || len(usage) != 0 || !called {
		t.Fatalf("usage=%v called=%v err=%v", usage, called, err)
	}
}
