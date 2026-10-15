package mihomo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"github.com/yyydddkkk/SwarmJet/internal/secretbox"
	"sync/atomic"
	"testing"
)

const directConfig = "mode: rule\nrules:\n  - MATCH,DIRECT\n"

type fakeCore struct {
	failValidate bool
	failProbe    bool
	last         *fakeInstance
}
type fakeInstance struct {
	alive     atomic.Bool
	failProbe bool
	chosen    string
	url       string
}

func (c *fakeCore) Validate(context.Context, string) error {
	if c.failValidate {
		return errors.New("invalid config")
	}
	return nil
}
func (c *fakeCore) Start(context.Context, string) (Instance, error) {
	p := &fakeInstance{failProbe: c.failProbe, chosen: "node-a"}
	p.alive.Store(true)
	c.last = p
	return p, nil
}
func (p *fakeInstance) Alive() bool      { return p.alive.Load() }
func (p *fakeInstance) ProxyURL() string { return p.url }
func (p *fakeInstance) Close()           { p.alive.Store(false) }
func (p *fakeInstance) Groups(context.Context) ([]Group, error) {
	return []Group{{Name: "select", Type: "Selector", Now: p.chosen, All: []string{"node-a", "node-b"}}}, nil
}
func (p *fakeInstance) Select(_ context.Context, g, n string) error { p.chosen = n; return nil }
func (p *fakeInstance) Probe(_ context.Context, url, name string) (Probe, error) {
	return Probe{URL: url, Proxy: name, OK: !p.failProbe, Status: 204}, nil
}
func testManager(t *testing.T) (*Manager, *fakeCore) {
	t.Helper()
	box, e := secretbox.New(strings.Repeat("a", 64))
	if e != nil {
		t.Fatal(e)
	}
	core := &fakeCore{}
	m, e := New(t.TempDir(), "http://proxy:17890", box, core)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(m.Close)
	return m, core
}
func importDraft(t *testing.T, m *Manager, name string) Revision {
	t.Helper()
	r, e := m.Import(name, "yaml", directConfig)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestProbeAndExplicitApplyPreserveLastWorkingConfig(t *testing.T) {
	m, core := testManager(t)
	ctx := context.Background()
	a := importDraft(t, m, "first")
	if m.View().Running {
		t.Fatal("import activated a core")
	}
	if e := m.Apply(ctx, a.ID); e == nil {
		t.Fatal("applied without probe")
	}
	if _, e := m.Probe(ctx, a.ID, "https://probe.test/", ""); e != nil {
		t.Fatal(e)
	}
	if m.View().Running {
		t.Fatal("probe changed live traffic")
	}
	if e := m.Apply(ctx, a.ID); e != nil {
		t.Fatal(e)
	}
	old := core.last
	b := importDraft(t, m, "second")
	core.failProbe = true
	r, e := m.Probe(ctx, b.ID, "https://probe.test/", "node-a")
	if e != nil || r.OK {
		t.Fatal(r, e)
	}
	if e = m.Apply(ctx, b.ID); e == nil {
		t.Fatal("applied failed probe")
	}
	if !old.Alive() || m.View().Active != a.ID {
		t.Fatal("failed probe evicted active")
	}
	core.failProbe = false
	if _, e = m.Probe(ctx, b.ID, "https://probe.test/", ""); e != nil {
		t.Fatal(e)
	}
	if e = m.Apply(ctx, b.ID); e != nil {
		t.Fatal(e)
	}
	if old.Alive() || m.View().Previous != a.ID {
		t.Fatal("old core not retired")
	}
	if e = m.Rollback(ctx, "https://probe.test/", ""); e != nil {
		t.Fatal(e)
	}
	if m.View().Active != a.ID {
		t.Fatal("rollback failed")
	}
}
func TestEncryptedHistoryAndExclusiveOwnership(t *testing.T) {
	m, _ := testManager(t)
	r, e := m.Import("private", "hy2", "hy2://never-store-plaintext@server.test")
	if e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(filepath.Join(m.dir, "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "never-store-plaintext") {
		t.Fatal("plaintext credentials on disk")
	}
	text, e := m.Configuration(r.ID)
	if e != nil || !strings.Contains(text, "never-store-plaintext") {
		t.Fatal("cannot recover configuration")
	}
	other, e := New(m.dir, "", m.box, &fakeCore{})
	if e == nil {
		other.Close()
		t.Fatal("two managers own the same directory")
	}
}
func TestRestartRestoresSelectionAndAppliedRevision(t *testing.T) {
	m, _ := testManager(t)
	ctx := context.Background()
	r := importDraft(t, m, "selected")
	_, _ = m.Probe(ctx, r.ID, "https://probe.test/", "")
	if e := m.Apply(ctx, r.ID); e != nil {
		t.Fatal(e)
	}
	if e := m.Select(ctx, "select", "node-b"); e != nil {
		t.Fatal(e)
	}
	dir, box := m.dir, m.box
	m.Close()
	core := &fakeCore{}
	restored, e := New(dir, "", box, core)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	restored.recover(ctx)
	if !restored.View().Running || core.last.chosen != "node-b" {
		t.Fatal("restart lost active selection")
	}
	if e = restored.Apply(ctx, r.ID); e == nil {
		t.Fatal("persisted old probe incorrectly authorizes new apply")
	}
}
