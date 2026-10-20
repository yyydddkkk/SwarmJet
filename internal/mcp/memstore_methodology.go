package mcp

import (
	"context"
	"strings"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/methodology"
)

// memMethodology is one stored version in MemoryBackend. The real store keeps
// the same data in two JSONB columns.
type memMethodology struct {
	version domain.Methodology
	files   []domain.MethodologyFile
}

func (m *MemoryBackend) PublishMethodology(ctx context.Context, v domain.Methodology, files []domain.MethodologyFile) (domain.Methodology, error) {
	if err := ctx.Err(); err != nil {
		return domain.Methodology{}, err
	}
	if err := methodology.Validate(v.Manifest, files); err != nil {
		return domain.Methodology{}, err
	}
	digest, err := methodology.Digest(v.Manifest, files)
	if err != nil {
		return domain.Methodology{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.hives[v.HiveID]; !ok {
		return domain.Methodology{}, domain.ErrNotFound
	}
	list := m.methodologies[v.HiveID]
	if v.Source != nil && strings.HasPrefix(v.Source.URL, "builtin:") && v.Source.Ref != "" {
		for _, item := range list {
			current := item.version
			if current.Active && current.Source != nil && current.Source.URL == v.Source.URL &&
				current.Source.Ref == v.Source.Ref && current.Digest == digest {
				return current, nil
			}
		}
	}
	for i := range list {
		list[i].version.Active = false
	}
	v.ID = m.newID("methodology")
	v.Version = len(list) + 1
	v.Active = true
	v.Digest = digest
	if v.CreatedAt.IsZero() {
		v.CreatedAt = m.now()
	}
	if v.CreatedBy == "" {
		v.CreatedBy = domain.AddressSystem
	}
	list = append(list, memMethodology{version: v, files: append([]domain.MethodologyFile(nil), files...)})
	m.methodologies[v.HiveID] = list
	m.recordMethodologyChangeLocked(v.HiveID, methodology.ActionPublish, v.Version, v.CreatedBy, v.Note)
	return v, nil
}

func (m *MemoryBackend) ActiveMethodology(ctx context.Context, hiveID string) (domain.Methodology, []domain.MethodologyFile, error) {
	if err := ctx.Err(); err != nil {
		return domain.Methodology{}, nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.methodologies[hiveID]
	for _, item := range list {
		if item.version.Active {
			return item.version, append([]domain.MethodologyFile(nil), item.files...), nil
		}
	}
	return domain.Methodology{}, nil, domain.ErrNotFound
}

func (m *MemoryBackend) MethodologyVersion(ctx context.Context, hiveID string, version int) (domain.Methodology, []domain.MethodologyFile, error) {
	if err := ctx.Err(); err != nil {
		return domain.Methodology{}, nil, err
	}
	if version <= 0 {
		return m.ActiveMethodology(ctx, hiveID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range m.methodologies[hiveID] {
		if item.version.Version == version {
			return item.version, append([]domain.MethodologyFile(nil), item.files...), nil
		}
	}
	return domain.Methodology{}, nil, domain.ErrNotFound
}

func (m *MemoryBackend) MethodologyVersions(ctx context.Context, hiveID string) ([]domain.Methodology, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.methodologies[hiveID]
	out := make([]domain.Methodology, 0, len(list))
	for i := len(list) - 1; i >= 0; i-- {
		out = append(out, list[i].version)
	}
	return out, nil
}

func (m *MemoryBackend) MethodologyChanges(ctx context.Context, hiveID string) ([]methodology.Change, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]methodology.Change(nil), m.methodologyChanges[hiveID]...), nil
}

func (m *MemoryBackend) ActivateMethodology(ctx context.Context, hiveID string, version int, actor domain.Address, note string) (domain.Methodology, error) {
	if err := ctx.Err(); err != nil {
		return domain.Methodology{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.methodologies[hiveID]
	target := -1
	for i := range list {
		if list[i].version.Version == version {
			target = i
		}
	}
	if target < 0 {
		return domain.Methodology{}, domain.ErrNotFound
	}
	for i := range list {
		list[i].version.Active = i == target
	}
	m.methodologies[hiveID] = list
	if actor == "" {
		actor = domain.AddressSystem
	}
	active := list[target].version
	m.recordMethodologyChangeLocked(hiveID, methodology.ActionActivate, version, actor, note)
	return active, nil
}

func (m *MemoryBackend) recordMethodologyChangeLocked(hiveID, action string, version int, actor domain.Address, note string) {
	m.methodologyChanges[hiveID] = append(m.methodologyChanges[hiveID], methodology.Change{
		ID:        int64(len(m.methodologyChanges[hiveID]) + 1),
		HiveID:    hiveID,
		Action:    action,
		Version:   version,
		Actor:     actor,
		Note:      note,
		CreatedAt: m.now(),
	})
}
