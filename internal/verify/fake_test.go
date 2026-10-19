package verify

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// fakeStore is the in-memory Store used by the scheduling tests. The real
// implementation lives in internal/store (round-4 task A); keeping this fake
// here makes the interface and the side effects under test explicit.
type fakeStore struct {
	mu       sync.Mutex
	seq      int
	hives    map[string]domain.Hive
	agents   map[string]domain.Agent
	messages []domain.Message
	msgsByID map[string]domain.Message
	verdicts []domain.Verdict
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		hives:    map[string]domain.Hive{},
		agents:   map[string]domain.Agent{},
		msgsByID: map[string]domain.Message{},
	}
}

func (f *fakeStore) addHive(h domain.Hive) domain.Hive {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h.ID == "" {
		f.seq++
		h.ID = fmt.Sprintf("hive-%d", f.seq)
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now().UTC()
	}
	f.hives[h.ID] = h
	return h
}

func (f *fakeStore) addAgent(a domain.Agent) domain.Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.ID == "" {
		f.seq++
		a.ID = fmt.Sprintf("agent-%d", f.seq)
	}
	if a.Role == "" {
		a.Role = domain.RoleForager
	}
	if a.State == "" {
		a.State = domain.AgentActive
	}
	if a.Kind == "" {
		a.Kind = domain.KindWorker
	}
	f.agents[a.ID] = a
	return a
}

func (f *fakeStore) Hive(_ context.Context, id string) (domain.Hive, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.hives[id]
	if !ok {
		return domain.Hive{}, domain.ErrNotFound
	}
	return h, nil
}

func (f *fakeStore) Agent(_ context.Context, id string) (domain.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[id]
	if !ok {
		return domain.Agent{}, domain.ErrNotFound
	}
	return a, nil
}

func (f *fakeStore) CreateForager(_ context.Context, a domain.Agent) (domain.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.ID == "" {
		f.seq++
		a.ID = fmt.Sprintf("agent-%d", f.seq)
	}
	if _, exists := f.agents[a.ID]; exists {
		return domain.Agent{}, domain.ErrConflict
	}
	if a.Role == "" {
		a.Role = domain.RoleForager
	}
	if a.State == "" {
		a.State = domain.AgentCreating
	}
	if a.Kind == "" {
		a.Kind = domain.KindWorker
	}
	if a.Branch == "" {
		a.Branch = "swarm/" + a.HiveID + "/" + a.ID
	}
	a.CreatedAt = time.Now().UTC()
	a.UpdatedAt = a.CreatedAt
	f.agents[a.ID] = a
	return a, nil
}

func (f *fakeStore) SendMessage(_ context.Context, m domain.Message) (domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if isForager(m.From) && isForager(m.To) {
		return domain.Message{}, domain.ErrPeerMessage
	}
	if m.ID == "" {
		f.seq++
		m.ID = fmt.Sprintf("msg-%d", f.seq)
	}
	if existing, ok := f.msgsByID[m.ID]; ok {
		return existing, nil
	}
	if m.Kind == "" {
		m.Kind = domain.MessageEvent
	}
	if m.State == "" {
		m.State = domain.MessagePending
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	f.messages = append(f.messages, m)
	f.msgsByID[m.ID] = m
	return m, nil
}

func (f *fakeStore) Inbox(_ context.Context, hiveID string, to domain.Address, afterID string, limit int) ([]domain.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	start := 0
	if afterID != "" {
		for i, m := range f.messages {
			if m.ID == afterID {
				start = i + 1
				break
			}
		}
	}
	var out []domain.Message
	for _, m := range f.messages[start:] {
		if m.HiveID == hiveID && m.To == to {
			out = append(out, m)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) RecordVerdict(_ context.Context, v domain.Verdict) (domain.Verdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.verdicts {
		if existing.HiveID == v.HiveID && existing.PRNumber == v.PRNumber &&
			existing.HeadSHA == v.HeadSHA && existing.VerifierID == v.VerifierID {
			return existing, nil
		}
	}
	if v.ID == "" {
		f.seq++
		v.ID = fmt.Sprintf("verdict-%d", f.seq)
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	f.verdicts = append(f.verdicts, v)
	return v, nil
}

func (f *fakeStore) SetVerdictComment(_ context.Context, id, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.verdicts {
		if f.verdicts[i].ID == id {
			f.verdicts[i].CommentURL = url
			return nil
		}
	}
	return domain.ErrNotFound
}

func (f *fakeStore) VerdictByVerifier(_ context.Context, verifierID string) (domain.Verdict, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.verdicts {
		if v.VerifierID == verifierID {
			return v, nil
		}
	}
	return domain.Verdict{}, domain.ErrNotFound
}

func (f *fakeStore) SupersedeVerdicts(_ context.Context, hiveID string, pr int64, keepSHA string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.verdicts {
		if f.verdicts[i].HiveID == hiveID && f.verdicts[i].PRNumber == pr && f.verdicts[i].HeadSHA != keepSHA {
			f.verdicts[i].Superseded = true
		}
	}
	return nil
}

func (f *fakeStore) CountVerdicts(_ context.Context, hiveID, ownerID string, result domain.VerdictResult) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, v := range f.verdicts {
		if v.HiveID == hiveID && v.OwnerID == ownerID && v.Result == result {
			count++
		}
	}
	return count, nil
}

func (f *fakeStore) Verifiers(_ context.Context, hiveID string, pr int64) ([]domain.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Agent
	for _, a := range f.agents {
		if a.HiveID == hiveID && a.PRNumber == pr && a.Kind == domain.KindVerifier &&
			!terminal(a.State) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeStore) messagesTo(hiveID string, to domain.Address) []domain.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Message
	for _, m := range f.messages {
		if m.HiveID == hiveID && m.To == to {
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeStore) allMessages() []domain.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Message(nil), f.messages...)
}

func (f *fakeStore) allVerdicts() []domain.Verdict {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Verdict(nil), f.verdicts...)
}

func (f *fakeStore) allAgents() []domain.Agent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Agent, 0, len(f.agents))
	for _, a := range f.agents {
		out = append(out, a)
	}
	return out
}

type fakeGitHub struct {
	head       string
	headErr    error
	commentURL string
	commentErr error
	comments   []string
}

func (f *fakeGitHub) PRHead(_ context.Context, _ string, _ int64) (string, error) {
	if f.headErr != nil {
		return "", f.headErr
	}
	return f.head, nil
}

func (f *fakeGitHub) CreatePRComment(_ context.Context, _ string, _ int64, body string) (string, error) {
	if f.commentErr != nil {
		return "", f.commentErr
	}
	f.comments = append(f.comments, body)
	if f.commentURL == "" {
		return "https://github.com/acme/hive/pull/7#issuecomment-1", nil
	}
	return f.commentURL, nil
}

func isForager(a domain.Address) bool {
	return len(a) > len("forager:") && string(a[:len("forager:")]) == "forager:"
}

func terminal(s domain.AgentState) bool {
	switch s {
	case domain.AgentCompleted, domain.AgentFailed, domain.AgentCancelled:
		return true
	default:
		return false
	}
}
