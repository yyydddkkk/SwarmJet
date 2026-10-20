package store

import (
	"context"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// SendMessages commits related messages and their delivery jobs together, so
// consumers cannot observe a lifecycle control without its accompanying event.
func (s *Store) SendMessages(ctx context.Context, messages ...domain.Message) ([]domain.Message, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	sent := make([]domain.Message, 0, len(messages))
	for _, message := range messages {
		m, err := s.sendMessageTx(ctx, tx, message)
		if err != nil {
			return nil, err
		}
		sent = append(sent, m)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return sent, nil
}
