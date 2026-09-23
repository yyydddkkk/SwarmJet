package store

import (
	"context"
	"reflect"
	"testing"
)

func TestEnabledAgentsExplicitAndPersistent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ids, err := s.EnabledAgents(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatal("agents implicitly enabled", ids, err)
	}
	if err = s.SetEnabledAgents(ctx, []string{"codex", "dst"}); err != nil {
		t.Fatal(err)
	}
	other := &Store{Pool: s.Pool}
	ids, err = other.EnabledAgents(ctx)
	if err != nil || !reflect.DeepEqual(ids, []string{"codex", "dst"}) {
		t.Fatal(ids, err)
	}
	for _, invalid := range [][]string{{"unknown"}, {"codex", "codex"}} {
		if err = s.SetEnabledAgents(ctx, invalid); err == nil {
			t.Fatal("invalid enabled list accepted")
		}
	}
	if err = s.SetEnabledAgents(ctx, nil); err != nil {
		t.Fatal(err)
	}
	ids, err = s.EnabledAgents(ctx)
	if err != nil || len(ids) != 0 {
		t.Fatal("disable all failed", ids, err)
	}
}
