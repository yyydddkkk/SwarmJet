package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// MemoryBackend is an in-memory Backend used by tests and by the first round of
// the MCP service. It enforces the same message validation rules as the real
// store contract:
//
//   - forager:* -> forager:*                returns domain.ErrPeerMessage
//   - Hop > hive MaxHops                returns domain.ErrHopLimit
//   - Body longer than MaxMessageBody   returns domain.ErrMessageTooLarge
//   - same ID, same content             returns the stored message
//   - same ID, different content        returns domain.ErrConflict
//   - CreateForager over the concurrent or per-chain limit returns
//     domain.ErrHiveLimit / domain.ErrChainLimit
//
// It is safe for concurrent use.
type MemoryBackend struct {
	mu  sync.Mutex
	now func() time.Time

	hives     map[string]domain.Hive
	hiveOrder []string

	agents     map[string]domain.Agent
	agentOrder []string

	messages map[string]domain.Message
	inboxes  map[string][]domain.Message // inbox key -> messages in write order
	waiters  map[string][]chan struct{}

	tokens map[string]domain.Token // sha256(plaintext) hex -> token
	claims map[string]claim

	seq int64
}

type claim struct {
	owner   string
	expires time.Time
}

// NewMemoryBackend returns an empty in-memory backend.
func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{
		now:      time.Now,
		hives:    map[string]domain.Hive{},
		agents:   map[string]domain.Agent{},
		messages: map[string]domain.Message{},
		inboxes:  map[string][]domain.Message{},
		waiters:  map[string][]chan struct{}{},
		tokens:   map[string]domain.Token{},
		claims:   map[string]claim{},
	}
}

// SetClock overrides the backend clock. It is intended for tests.
func (m *MemoryBackend) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	m.mu.Lock()
	m.now = now
	m.mu.Unlock()
}

// CreateToken mints a token and returns its plaintext once. It mirrors the
// contract signature but is not part of Backend because no MCP tool needs it;
// the fake exposes it so tests (and a later bootstrap path) can obtain tokens.
func (m *MemoryBackend) CreateToken(_ context.Context, name string, scope domain.TokenScope) (string, domain.Token, error) {
	if scope == "" {
		scope = domain.TokenDefault
	}
	plaintext := "swj_" + randomHex(24)
	hash := sha256.Sum256([]byte(plaintext))
	m.mu.Lock()
	t := domain.Token{ID: m.newID("tok"), Name: name, Scope: scope, CreatedAt: m.now()}
	m.tokens[hex.EncodeToString(hash[:])] = t
	m.mu.Unlock()
	return plaintext, t, nil
}

// CreateAgentToken mints an agent-scoped token for an existing Agent. The
// plaintext is returned once and only its hash is kept, matching the store
// contract.
func (m *MemoryBackend) CreateAgentToken(_ context.Context, agentID string) (string, domain.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.agents[agentID]; !ok {
		return "", domain.Token{}, domain.ErrNotFound
	}
	plaintext := "swj_" + randomHex(24)
	hash := sha256.Sum256([]byte(plaintext))
	t := domain.Token{ID: m.newID("tok"), Name: "agent:" + agentID, Scope: domain.TokenAgent, AgentID: agentID, CreatedAt: m.now()}
	m.tokens[hex.EncodeToString(hash[:])] = t
	return plaintext, t, nil
}

// CreateHive stores a hive and creates its disabled Queen record.
func (m *MemoryBackend) CreateHive(ctx context.Context, h domain.Hive) (domain.Hive, error) {
	if err := ctx.Err(); err != nil {
		return domain.Hive{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if h.ID == "" {
		h.ID = m.newID("hive")
	}
	if _, exists := m.hives[h.ID]; exists {
		return domain.Hive{}, domain.ErrConflict
	}
	now := m.now()
	h.Config = withConfigDefaults(h.Config)
	h.Version = 1
	h.CreatedAt = now
	h.UpdatedAt = now
	m.hives[h.ID] = h
	m.hiveOrder = append(m.hiveOrder, h.ID)

	queen := domain.Agent{
		ID:        m.newID("queen"),
		HiveID:    h.ID,
		Role:      domain.RoleQueen,
		State:     domain.AgentDisabled,
		CLI:       h.Config.CLI,
		CreatedAt: now,
		UpdatedAt: now,
	}
	m.agents[queen.ID] = queen
	m.agentOrder = append(m.agentOrder, queen.ID)
	return h, nil
}

// Hive returns a hive with its config defaults applied.
func (m *MemoryBackend) Hive(ctx context.Context, id string) (domain.Hive, error) {
	if err := ctx.Err(); err != nil {
		return domain.Hive{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hives[id]
	if !ok {
		return domain.Hive{}, domain.ErrNotFound
	}
	h.Config = withConfigDefaults(h.Config)
	return h, nil
}

// Hives lists hives in creation order.
func (m *MemoryBackend) Hives(ctx context.Context) ([]domain.Hive, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.Hive, 0, len(m.hiveOrder))
	for _, id := range m.hiveOrder {
		h := m.hives[id]
		h.Config = withConfigDefaults(h.Config)
		out = append(out, h)
	}
	return out, nil
}

// UpdateHive applies an optimistic version check.
func (m *MemoryBackend) UpdateHive(ctx context.Context, h domain.Hive) (domain.Hive, error) {
	if err := ctx.Err(); err != nil {
		return domain.Hive{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.hives[h.ID]
	if !ok {
		return domain.Hive{}, domain.ErrNotFound
	}
	if h.Version != existing.Version {
		return domain.Hive{}, domain.ErrConflict
	}
	h.Config = withConfigDefaults(h.Config)
	h.Version = existing.Version + 1
	h.CreatedAt = existing.CreatedAt
	h.UpdatedAt = m.now()
	m.hives[h.ID] = h
	return h, nil
}

// CreateForager enforces the concurrent and per-chain forager limits.
func (m *MemoryBackend) CreateForager(ctx context.Context, a domain.Agent) (domain.Agent, error) {
	if err := ctx.Err(); err != nil {
		return domain.Agent{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hives[a.HiveID]
	if !ok {
		return domain.Agent{}, domain.ErrNotFound
	}
	cfg := withConfigDefaults(h.Config)

	if a.ID == "" {
		a.ID = m.newID("forager")
	}
	if a.Role == "" {
		a.Role = domain.RoleForager
	}
	if a.Role != domain.RoleForager {
		return domain.Agent{}, fmt.Errorf("mcp: CreateForager called with role %q", a.Role)
	}
	if a.State == "" {
		a.State = domain.AgentCreating
	}
	if a.CLI == "" {
		a.CLI = cfg.CLI
	}
	if a.Branch == "" {
		a.Branch = fmt.Sprintf("swarm/%s/%s", a.HiveID, a.ID)
	}
	if a.ChainID == "" {
		a.ChainID = m.newID("chain")
	}

	active, chained := 0, 0
	for _, existing := range m.agents {
		if existing.HiveID != a.HiveID || existing.Role != domain.RoleForager {
			continue
		}
		if foragerCountsTowardsLimit(existing.State) {
			active++
		}
		if existing.ChainID == a.ChainID {
			chained++
		}
	}
	if active >= cfg.MaxConcurrentForagers {
		return domain.Agent{}, domain.ErrHiveLimit
	}
	if chained >= cfg.MaxForagersPerChain {
		return domain.Agent{}, domain.ErrChainLimit
	}

	now := m.now()
	a.CreatedAt = now
	a.UpdatedAt = now
	m.agents[a.ID] = a
	m.agentOrder = append(m.agentOrder, a.ID)
	return a, nil
}

// Agent returns any Queen or Forager by ID.
func (m *MemoryBackend) Agent(ctx context.Context, id string) (domain.Agent, error) {
	if err := ctx.Err(); err != nil {
		return domain.Agent{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok {
		return domain.Agent{}, domain.ErrNotFound
	}
	return a, nil
}

// Queen returns the single Queen record of a hive.
func (m *MemoryBackend) Queen(ctx context.Context, hiveID string) (domain.Agent, error) {
	if err := ctx.Err(); err != nil {
		return domain.Agent{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.agentOrder {
		a := m.agents[id]
		if a.HiveID == hiveID && a.Role == domain.RoleQueen {
			return a, nil
		}
	}
	return domain.Agent{}, domain.ErrNotFound
}

// Foragers lists a hive's foragers, optionally filtered by state.
func (m *MemoryBackend) Foragers(ctx context.Context, hiveID string, states ...domain.AgentState) ([]domain.Agent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.Agent{}
	for _, id := range m.agentOrder {
		a := m.agents[id]
		if a.HiveID != hiveID || a.Role != domain.RoleForager {
			continue
		}
		if len(states) > 0 && !slices.Contains(states, a.State) {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// TransitionAgent is a compare-and-set: it fails with ErrConflict unless the
// agent is currently in the from state.
func (m *MemoryBackend) TransitionAgent(ctx context.Context, id string, from, to domain.AgentState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok {
		return domain.ErrNotFound
	}
	if a.State != from {
		return domain.ErrConflict
	}
	a.State = to
	a.UpdatedAt = m.now()
	m.agents[id] = a
	return nil
}

// UpdateHandoff replaces an Agent's handoff summary.
func (m *MemoryBackend) UpdateHandoff(ctx context.Context, id string, h domain.Handoff) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.agents[id]
	if !ok {
		return domain.ErrNotFound
	}
	handoff := h
	a.Handoff = &handoff
	a.UpdatedAt = m.now()
	m.agents[id] = a
	return nil
}

// SendMessage validates, stores and fans out a message, notifying waiters in
// the same critical section so no wakeup is lost.
func (m *MemoryBackend) SendMessage(ctx context.Context, msg domain.Message) (domain.Message, error) {
	if err := ctx.Err(); err != nil {
		return domain.Message{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	h, ok := m.hives[msg.HiveID]
	if !ok {
		return domain.Message{}, domain.ErrNotFound
	}
	cfg := withConfigDefaults(h.Config)

	if utf8.RuneCountInString(msg.Body) > domain.MaxMessageBody {
		return domain.Message{}, domain.ErrMessageTooLarge
	}
	if msg.Hop > cfg.MaxHops {
		return domain.Message{}, domain.ErrHopLimit
	}
	if isForagerAddress(msg.From) && isForagerAddress(msg.To) {
		return domain.Message{}, domain.ErrPeerMessage
	}

	if msg.ID != "" {
		if existing, ok := m.messages[msg.ID]; ok {
			if sameMessage(existing, msg) {
				return existing, nil
			}
			return domain.Message{}, domain.ErrConflict
		}
	} else {
		msg.ID = m.newID("msg")
	}
	if msg.Kind == "" {
		msg.Kind = domain.MessageEvent
	}
	if msg.State == "" {
		msg.State = domain.MessagePending
	}
	msg.CreatedAt = m.now()
	msg.Artifacts = slices.Clone(msg.Artifacts)

	m.messages[msg.ID] = msg
	key := inboxKey(msg.HiveID, msg.To)
	m.inboxes[key] = append(m.inboxes[key], msg)
	m.notifyLocked(key)
	return msg, nil
}

// Inbox returns messages addressed to to, in write order, strictly after
// afterID when it names a stored message.
func (m *MemoryBackend) Inbox(ctx context.Context, hiveID string, to domain.Address, afterID string, limit int) ([]domain.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.inboxes[inboxKey(hiveID, to)]
	start := 0
	if afterID != "" {
		for i, msg := range msgs {
			if msg.ID == afterID {
				start = i + 1
				break
			}
		}
	}
	out := slices.Clone(msgs[start:])
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ClaimMessage takes an owner lease on a message. A live lease held by another
// owner blocks the claim; an expired lease can be taken over. The same owner can
// always renew.
func (m *MemoryBackend) ClaimMessage(ctx context.Context, id, owner string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	c, ok := m.claims[id]
	switch {
	case !ok:
	case c.owner == owner:
	case now.Before(c.expires):
		return false, nil
	}
	m.claims[id] = claim{owner: owner, expires: now.Add(ttl)}
	return true, nil
}

// WaitInbox blocks until a message newer than afterID exists in the inbox, or
// until ctx ends.
func (m *MemoryBackend) WaitInbox(ctx context.Context, hiveID string, to domain.Address, afterID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := inboxKey(hiveID, to)

	m.mu.Lock()
	if m.hasNewerLocked(key, afterID) {
		m.mu.Unlock()
		return nil
	}
	ch := make(chan struct{}, 1)
	m.waiters[key] = append(m.waiters[key], ch)
	m.mu.Unlock()

	select {
	case <-ctx.Done():
		m.mu.Lock()
		m.removeWaiterLocked(key, ch)
		m.mu.Unlock()
		return ctx.Err()
	case <-ch:
		return nil
	}
}

// TokenByPlaintext resolves a bearer token by hashing the presented plaintext.
func (m *MemoryBackend) TokenByPlaintext(ctx context.Context, plaintext string) (domain.Token, error) {
	if err := ctx.Err(); err != nil {
		return domain.Token{}, err
	}
	hash := sha256.Sum256([]byte(plaintext))
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[hex.EncodeToString(hash[:])]
	if !ok {
		return domain.Token{}, domain.ErrNotFound
	}
	return t, nil
}

func (m *MemoryBackend) hasNewerLocked(key, afterID string) bool {
	msgs := m.inboxes[key]
	if afterID == "" {
		return len(msgs) > 0
	}
	for i, msg := range msgs {
		if msg.ID == afterID {
			return i < len(msgs)-1
		}
	}
	// Unknown cursor: be conservative and report whatever is there.
	return len(msgs) > 0
}

func (m *MemoryBackend) notifyLocked(key string) {
	for _, ch := range m.waiters[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	m.waiters[key] = nil
}

func (m *MemoryBackend) removeWaiterLocked(key string, target chan struct{}) {
	waiters := m.waiters[key]
	for i, ch := range waiters {
		if ch == target {
			m.waiters[key] = append(waiters[:i:i], waiters[i+1:]...)
			return
		}
	}
}

func (m *MemoryBackend) newID(prefix string) string {
	m.seq++
	return fmt.Sprintf("%s_%s%04d", prefix, randomHex(4), m.seq)
}

func inboxKey(hiveID string, to domain.Address) string {
	return hiveID + "\x00" + string(to)
}

func isForagerAddress(a domain.Address) bool {
	return strings.HasPrefix(string(a), "forager:")
}

// foragerCountsTowardsLimit reports the states the contract counts against the
// per-hive concurrent forager limit.
func foragerCountsTowardsLimit(s domain.AgentState) bool {
	switch s {
	case domain.AgentCreating, domain.AgentActive, domain.AgentParked:
		return true
	default:
		return false
	}
}

func sameMessage(a, b domain.Message) bool {
	return a.HiveID == b.HiveID &&
		a.From == b.From &&
		a.To == b.To &&
		a.Kind == b.Kind &&
		a.ChainID == b.ChainID &&
		a.Hop == b.Hop &&
		a.Body == b.Body &&
		slices.Equal(a.Artifacts, b.Artifacts)
}

// withConfigDefaults fills zero HiveConfig fields with the contract defaults.
func withConfigDefaults(c domain.HiveConfig) domain.HiveConfig {
	if c.CLI == "" {
		c.CLI = "opencode"
	}
	if c.MaxConcurrentForagers <= 0 {
		c.MaxConcurrentForagers = 4
	}
	if c.MaxHops <= 0 {
		c.MaxHops = 10
	}
	if c.MaxForagersPerChain <= 0 {
		c.MaxForagersPerChain = 10
	}
	if c.IdleParkMinutes <= 0 {
		c.IdleParkMinutes = 30
	}
	if c.HibernateHours <= 0 {
		c.HibernateHours = 24
	}
	if c.TurnTimeoutMinutes <= 0 {
		c.TurnTimeoutMinutes = 120
	}
	if c.ForagerMaxLifetimeHours <= 0 {
		c.ForagerMaxLifetimeHours = 168
	}
	return c
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail in practice; keep the server running.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
