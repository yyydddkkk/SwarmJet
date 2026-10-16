package router

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// MemoryBackend is an in-memory Backend used by tests and by the first-round
// Router demo. It enforces the same message validation rules the real store
// must enforce, so routing behaviour is identical.
type MemoryBackend struct {
	mu           sync.Mutex
	hives        map[string]domain.Hive
	hiveByRepo   map[string]string
	agents       map[string]domain.Agent
	agentOrder   []string
	messages     []domain.Message
	messagesByID map[string]domain.Message
	seq          int
}

// NewMemoryBackend returns an empty backend.
func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{
		hives:        map[string]domain.Hive{},
		hiveByRepo:   map[string]string{},
		agents:       map[string]domain.Agent{},
		messagesByID: map[string]domain.Message{},
	}
}

// AddHive registers a Hive for the repository. It returns the stored Hive with
// defaulted config, mirroring the store's read path.
func (m *MemoryBackend) AddHive(h domain.Hive) domain.Hive {
	m.mu.Lock()
	defer m.mu.Unlock()
	if h.ID == "" {
		m.seq++
		h.ID = fmt.Sprintf("hive-%d", m.seq)
	}
	now := time.Now().UTC()
	if h.CreatedAt.IsZero() {
		h.CreatedAt = now
	}
	if h.UpdatedAt.IsZero() {
		h.UpdatedAt = now
	}
	m.hives[h.ID] = h
	if h.Repository != "" {
		m.hiveByRepo[h.Repository] = h.ID
	}
	return h
}

// AddDrone seeds a Drone without applying creation limits. Generated fields
// match CreateDrone so tests can build fixtures quickly.
func (m *MemoryBackend) AddDrone(a domain.Agent) domain.Agent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addDroneLocked(a)
}

// Hive returns a stored Hive by ID.
func (m *MemoryBackend) Hive(id string) (domain.Hive, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hives[id]
	return h, ok
}

// Drones returns every Drone of a Hive in insertion order.
func (m *MemoryBackend) Drones(hiveID string) []domain.Agent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.Agent{}
	for _, id := range m.agentOrder {
		if a := m.agents[id]; a.HiveID == hiveID {
			out = append(out, a)
		}
	}
	return out
}

// Messages returns a copy of every stored message in insertion order.
func (m *MemoryBackend) Messages() []domain.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domain.Message(nil), m.messages...)
}

// MessagesTo returns messages addressed to one address inside a Hive.
func (m *MemoryBackend) MessagesTo(hiveID string, to domain.Address) []domain.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.Message{}
	for _, msg := range m.messages {
		if msg.HiveID == hiveID && msg.To == to {
			out = append(out, msg)
		}
	}
	return out
}

// HiveByRepository implements Backend.
func (m *MemoryBackend) HiveByRepository(ctx context.Context, repository string) (domain.Hive, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.hiveByRepo[repository]
	if !ok {
		return domain.Hive{}, domain.ErrNotFound
	}
	h, ok := m.hives[id]
	if !ok {
		return domain.Hive{}, domain.ErrNotFound
	}
	h.Config = resolveConfig(h.Config)
	return h, nil
}

// DroneByIssue implements Backend: newest non-terminal Drone for the Issue.
func (m *MemoryBackend) DroneByIssue(ctx context.Context, hiveID string, issue int64) (domain.Agent, error) {
	return m.newestNonTerminal(func(a domain.Agent) bool {
		return a.HiveID == hiveID && a.Role == domain.RoleDrone && a.IssueNumber == issue
	})
}

// DroneByPR implements Backend: newest non-terminal Drone for the PR.
func (m *MemoryBackend) DroneByPR(ctx context.Context, hiveID string, pr int64) (domain.Agent, error) {
	return m.newestNonTerminal(func(a domain.Agent) bool {
		return a.HiveID == hiveID && a.Role == domain.RoleDrone && a.PRNumber == pr
	})
}

// DroneByBranch implements Backend: newest non-terminal Drone for the branch.
func (m *MemoryBackend) DroneByBranch(ctx context.Context, hiveID, branch string) (domain.Agent, error) {
	return m.newestNonTerminal(func(a domain.Agent) bool {
		return a.HiveID == hiveID && a.Role == domain.RoleDrone && a.Branch == branch
	})
}

// CreateDrone implements Backend. It enforces the Hive concurrency and chain
// limits and fills the generated ID and branch.
func (m *MemoryBackend) CreateDrone(ctx context.Context, a domain.Agent) (domain.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hive, ok := m.hives[a.HiveID]
	if !ok {
		return domain.Agent{}, domain.ErrNotFound
	}
	cfg := resolveConfig(hive.Config)
	active := 0
	for _, drone := range m.agents {
		if drone.HiveID != a.HiveID || drone.Role != domain.RoleDrone {
			continue
		}
		if drone.State == domain.AgentCreating || drone.State == domain.AgentActive || drone.State == domain.AgentParked {
			active++
		}
	}
	if active >= cfg.MaxConcurrentDrones {
		return domain.Agent{}, domain.ErrHiveLimit
	}
	if a.ChainID != "" {
		chained := 0
		for _, drone := range m.agents {
			if drone.HiveID == a.HiveID && drone.Role == domain.RoleDrone && drone.ChainID == a.ChainID {
				chained++
			}
		}
		if chained >= cfg.MaxDronesPerChain {
			return domain.Agent{}, domain.ErrChainLimit
		}
	}
	return m.addDroneLocked(a), nil
}

// SendMessage implements Backend: validate, dedupe by ID, then append. A
// repeated ID with identical content returns the stored message; a repeated ID
// with different content returns domain.ErrConflict.
func (m *MemoryBackend) SendMessage(ctx context.Context, msg domain.Message) (domain.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.validateLocked(msg); err != nil {
		return domain.Message{}, err
	}
	if msg.ID == "" {
		m.seq++
		msg.ID = fmt.Sprintf("msg-%d", m.seq)
	}
	if existing, ok := m.messagesByID[msg.ID]; ok {
		if !sameMessageContent(existing, msg) {
			return domain.Message{}, domain.ErrConflict
		}
		return existing, nil
	}
	if msg.State == "" {
		msg.State = domain.MessagePending
	}
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now().UTC()
	}
	m.messages = append(m.messages, msg)
	m.messagesByID[msg.ID] = msg
	return msg, nil
}

func (m *MemoryBackend) validateLocked(msg domain.Message) error {
	if isDroneAddress(msg.From) && isDroneAddress(msg.To) {
		return domain.ErrPeerMessage
	}
	maxHops := resolveConfig(domain.HiveConfig{}).MaxHops
	if hive, ok := m.hives[msg.HiveID]; ok {
		maxHops = resolveConfig(hive.Config).MaxHops
	}
	if msg.Hop > maxHops {
		return domain.ErrHopLimit
	}
	if utf8.RuneCountInString(msg.Body) > domain.MaxMessageBody {
		return domain.ErrMessageTooLarge
	}
	return nil
}

func (m *MemoryBackend) addDroneLocked(a domain.Agent) domain.Agent {
	if a.ID == "" {
		m.seq++
		a.ID = fmt.Sprintf("drone-%d", m.seq)
	}
	if a.Role == "" {
		a.Role = domain.RoleDrone
	}
	if a.State == "" {
		a.State = domain.AgentCreating
	}
	if a.CLI == "" {
		if hive, ok := m.hives[a.HiveID]; ok {
			a.CLI = resolveConfig(hive.Config).CLI
		} else {
			a.CLI = resolveConfig(domain.HiveConfig{}).CLI
		}
	}
	if a.Branch == "" {
		a.Branch = fmt.Sprintf("swarm/%s/%s", a.HiveID, a.ID)
	}
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	if _, exists := m.agents[a.ID]; !exists {
		m.agentOrder = append(m.agentOrder, a.ID)
	}
	m.agents[a.ID] = a
	return a
}

func (m *MemoryBackend) newestNonTerminal(match func(domain.Agent) bool) (domain.Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	candidates := []domain.Agent{}
	for _, id := range m.agentOrder {
		a := m.agents[id]
		if isTerminal(a.State) || !match(a) {
			continue
		}
		candidates = append(candidates, a)
	}
	if len(candidates) == 0 {
		return domain.Agent{}, domain.ErrNotFound
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt.Equal(candidates[j].CreatedAt) {
			return candidates[i].ID > candidates[j].ID
		}
		return candidates[i].CreatedAt.After(candidates[j].CreatedAt)
	})
	return candidates[0], nil
}

func isDroneAddress(a domain.Address) bool {
	return len(a) > len("drone:") && string(a[:len("drone:")]) == "drone:"
}

// sameMessageContent compares the semantic payload of two messages, ignoring
// the idempotency key, state, and timestamp.
func sameMessageContent(a, b domain.Message) bool {
	return a.HiveID == b.HiveID &&
		a.From == b.From &&
		a.To == b.To &&
		a.Kind == b.Kind &&
		a.ChainID == b.ChainID &&
		a.Hop == b.Hop &&
		a.Body == b.Body &&
		reflect.DeepEqual(a.Artifacts, b.Artifacts)
}
