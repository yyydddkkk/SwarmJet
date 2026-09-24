package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPlatformImageDeletionGuardProtectsRegisteredNode(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ref := "registry.test/swarmjet/platform@sha256:" + strings.Repeat("a", 64)
	_, err := s.Pool.Exec(ctx, `INSERT INTO nodes(id,address,capacity,last_seen,readiness) VALUES('remote1','http://remote1',1,now(),$1)`, `{"platformImage":"`+ref+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	usage, err := s.WithPlatformImageDeletionGuard(ctx, ref, func() error { called = true; return nil })
	if !errors.Is(err, ErrConflict) || called || len(usage) != 1 || usage[0] != "节点 remote1" {
		t.Fatalf("usage=%v called=%v err=%v", usage, called, err)
	}
	_, err = s.Pool.Exec(ctx, `UPDATE nodes SET readiness='{}' WHERE id='remote1'`)
	if err != nil {
		t.Fatal(err)
	}
	usage, err = s.WithPlatformImageDeletionGuard(ctx, ref, func() error { called = true; return nil })
	if err != nil || !called || len(usage) != 0 {
		t.Fatalf("usage=%v called=%v err=%v", usage, called, err)
	}
}
