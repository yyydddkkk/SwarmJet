package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
	"github.com/yyydddkkk/SwarmJet/internal/jobs"
)

// inboxChannel carries LISTEN/NOTIFY wakeups. The payload is best-effort:
// waiters always re-read the messages table, so a missed payload is harmless.
const inboxChannel = "swarmjet_inbox"

const messageColumns = `id,hive_id,from_addr,to_addr,kind,chain_id,hop,body,artifacts,trust,state,created_at`

// defaultMessageTrust fills an omitted provenance from the sender. A local
// token is the user; any Agent is another Agent; the system address is the
// platform. Router-generated messages set external explicitly because their
// text came from GitHub whatever the sender address says.
func defaultMessageTrust(from domain.Address) domain.MessageTrust {
	switch {
	case strings.HasPrefix(string(from), "local:"):
		return domain.TrustUser
	case from == domain.AddressSystem:
		return domain.TrustSystem
	case from == domain.AddressQueen, isForagerAddress(from):
		return domain.TrustAgent
	default:
		return domain.TrustAgent
	}
}

var messageSeq uint64

// newMessageID generates an ID for callers that do not supply one. Ordering no
// longer depends on this format: the messages.seq column is the Inbox cursor.
func newMessageID() (string, error) {
	id, err := newOpaqueID()
	if err != nil {
		return "", err
	}
	seq := atomic.AddUint64(&messageSeq, 1)
	return fmt.Sprintf("%013x-%06x-%s", time.Now().UTC().UnixMilli(), seq&0xffffff, id), nil
}

func scanMessage(row pgx.Row) (domain.Message, error) {
	var m domain.Message
	var from, to, kind, trust, state string
	var artifacts []byte
	err := row.Scan(&m.ID, &m.HiveID, &from, &to, &kind, &m.ChainID, &m.Hop, &m.Body, &artifacts, &trust, &state, &m.CreatedAt)
	if err != nil {
		return domain.Message{}, domainErr(err)
	}
	m.From, m.To, m.Kind, m.Trust, m.State = domain.Address(from), domain.Address(to), domain.MessageKind(kind), domain.MessageTrust(trust), domain.MessageState(state)
	if len(artifacts) > 0 {
		if err = json.Unmarshal(artifacts, &m.Artifacts); err != nil {
			return domain.Message{}, err
		}
	}
	return m, nil
}

func isForagerAddress(address domain.Address) bool {
	return len(address) > len("forager:") && string(address[:len("forager:")]) == "forager:"
}

// sameMessageContent compares the immutable part of a message. State and
// CreatedAt are excluded so a redelivery of the same content is idempotent.
func sameMessageContent(a, b domain.Message) bool {
	return a.HiveID == b.HiveID && a.From == b.From && a.To == b.To && a.Kind == b.Kind &&
		a.ChainID == b.ChainID && a.Hop == b.Hop && a.Body == b.Body && a.Trust == b.Trust &&
		reflect.DeepEqual(a.Artifacts, b.Artifacts)
}

func notifyPayload(hiveID string, to domain.Address) string {
	return hiveID + "\t" + string(to)
}

// SendMessage validates and stores a message, inserting its delivery River job
// in the same transaction. It is idempotent by ID: the same ID with the same
// content returns the stored message, while the same ID with different content
// is a conflict.
func (s *Store) SendMessage(ctx context.Context, m domain.Message) (domain.Message, error) {
	messages, err := s.SendMessages(ctx, m)
	if err != nil {
		return domain.Message{}, err
	}
	return messages[0], nil
}

func (s *Store) sendMessageTx(ctx context.Context, tx pgx.Tx, m domain.Message) (domain.Message, error) {
	if utf8.RuneCountInString(m.Body) > domain.MaxMessageBody {
		return domain.Message{}, domain.ErrMessageTooLarge
	}
	hive, err := scanHive(tx.QueryRow(ctx, `SELECT `+hiveColumns+` FROM hives WHERE id=$1`, m.HiveID))
	if err != nil {
		return domain.Message{}, err
	}
	if hive.QueenEnabled && m.To == domain.AddressHive && m.From != domain.AddressQueen {
		m.To = domain.AddressQueen
	}
	if m.Hop > hive.Config.MaxHops {
		return domain.Message{}, domain.ErrHopLimit
	}
	if m.ID == "" {
		id, e := newMessageID()
		if e != nil {
			return domain.Message{}, e
		}
		m.ID = id
	}
	if m.State == "" {
		m.State = domain.MessagePending
	}
	if m.Trust == "" {
		m.Trust = defaultMessageTrust(m.From)
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	var artifacts []byte
	if m.Artifacts != nil {
		if artifacts, err = json.Marshal(m.Artifacts); err != nil {
			return domain.Message{}, err
		}
	}
	// seq is assigned at INSERT time but Inbox readers walk it by commit order.
	// Serializing inserts per Hive makes the two orders identical: a reader can
	// never observe a later seq and then skip a concurrent transaction that
	// committed an earlier one. The lock is released with this transaction.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "inbox:"+m.HiveID); err != nil {
		return domain.Message{}, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO messages(`+messageColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT(id) DO NOTHING`,
		m.ID, m.HiveID, string(m.From), string(m.To), string(m.Kind), m.ChainID, m.Hop, m.Body, artifacts, string(m.Trust), string(m.State), m.CreatedAt)
	if err != nil {
		return domain.Message{}, domainErr(err)
	}
	if tag.RowsAffected() == 0 {
		existing, e := scanMessage(tx.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE id=$1`, m.ID))
		if e != nil {
			return domain.Message{}, e
		}
		if !sameMessageContent(existing, m) {
			return domain.Message{}, domain.ErrConflict
		}
		return existing, nil
	}
	if err = s.insertMessageJob(ctx, tx, m.ID); err != nil {
		return domain.Message{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_notify($1,$2)`, inboxChannel, notifyPayload(m.HiveID, m.To)); err != nil {
		return domain.Message{}, err
	}
	return m, nil
}

// messageInserter is the optional River wiring for message delivery. The
// Inserter interface is intentionally not extended so existing fakes keep
// compiling; *jobs.Client implements this method.
type messageInserter interface {
	InsertMessage(context.Context, pgx.Tx, jobs.Message) error
}

func (s *Store) insertMessageJob(ctx context.Context, tx pgx.Tx, id string) error {
	inserter, ok := s.Jobs.(messageInserter)
	if !ok || inserter == nil {
		return ErrMessageQueueUnavailable
	}
	return inserter.InsertMessage(ctx, tx, jobs.Message{ID: id})
}

// messageSeqForID resolves an Inbox cursor to the message's insertion
// sequence. ids are caller-supplied, so text order cannot stand in for
// insertion order; a cursor that does not exist cannot be resumed and yields
// domain.ErrNotFound.
func (s *Store) messageSeqForID(ctx context.Context, id string) (int64, error) {
	var seq int64
	if err := s.Pool.QueryRow(ctx, `SELECT seq FROM messages WHERE id=$1`, id).Scan(&seq); err != nil {
		return 0, domainErr(err)
	}
	return seq, nil
}

// Inbox returns messages for one address in insertion order, starting after
// afterID. The cursor is messages.seq, not the caller-supplied ID.
// MaxReceivedHop returns the largest hop among messages to `to` in chainID,
// or 0 when there are none.
func (s *Store) MaxReceivedHop(ctx context.Context, hiveID string, to domain.Address, chainID string) (int, error) {
	var hop int
	err := s.Pool.QueryRow(ctx, `SELECT coalesce(max(hop),0) FROM messages WHERE hive_id=$1 AND to_addr=$2 AND chain_id=$3`, hiveID, string(to), chainID).Scan(&hop)
	return hop, err
}

func (s *Store) Inbox(ctx context.Context, hiveID string, to domain.Address, afterID string, limit int) ([]domain.Message, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT ` + messageColumns + ` FROM messages WHERE hive_id=$1 AND to_addr=$2`
	args := []any{hiveID, string(to)}
	if afterID != "" {
		seq, err := s.messageSeqForID(ctx, afterID)
		if err != nil {
			return nil, err
		}
		query += ` AND seq>$3`
		args = append(args, seq)
	}
	query += fmt.Sprintf(` ORDER BY seq ASC LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []domain.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// Unhandled returns the messages waiting for one address: pending or delivered,
// in insertion order. Handled messages are omitted so a resumed Agent never
// replays finished work.
func (s *Store) Unhandled(ctx context.Context, hiveID string, to domain.Address, limit int) ([]domain.Message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+messageColumns+` FROM messages
		WHERE hive_id=$1 AND to_addr=$2 AND state IN ('pending','delivered')
		ORDER BY seq ASC LIMIT $3`, hiveID, string(to), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []domain.Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// inboxHasAfter reports whether a message newer than the cursor exists. It
// compares messages.seq, matching Inbox, because IDs are not ordered.
func (s *Store) inboxHasAfter(ctx context.Context, hiveID string, to domain.Address, afterID string) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM messages WHERE hive_id=$1 AND to_addr=$2`
	args := []any{hiveID, string(to)}
	if afterID != "" {
		seq, err := s.messageSeqForID(ctx, afterID)
		if err != nil {
			return false, err
		}
		query += ` AND seq>$3`
		args = append(args, seq)
	}
	query += `)`
	var exists bool
	if err := s.Pool.QueryRow(ctx, query, args...).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// ClaimMessage takes the Hive Inbox claim lease. Only one owner succeeds while
// the lease is live; an expired or self-owned lease can be renewed.
func (s *Store) ClaimMessage(ctx context.Context, id, owner string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE messages SET claimed_by=$2,claim_expires_at=now()+make_interval(secs=>$3)
		WHERE id=$1 AND state<>'handled' AND (claimed_by='' OR claimed_by=$2 OR claim_expires_at IS NULL OR claim_expires_at<now())`,
		id, owner, ttl.Seconds())
	if err != nil {
		return false, domainErr(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) MarkMessageHandled(ctx context.Context, id string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE messages SET state='handled' WHERE id=$1`, id)
	if err != nil {
		return domainErr(err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE id=$1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return domain.ErrNotFound
		}
	}
	return nil
}

// WaitInbox blocks until a message newer than afterID exists for the address or
// ctx ends. It compares insertion sequence, so a caller-supplied cursor works;
// a cursor that does not exist returns domain.ErrNotFound.
//
// Waiters share one Store-wide LISTEN connection instead of each holding a pool
// connection. The listener wakes the matching (Hive, address) waiters; every
// waiter also re-checks periodically, so a missed notification during a
// reconnect only delays the wake instead of losing it.
func (s *Store) WaitInbox(ctx context.Context, hiveID string, to domain.Address, afterID string) error {
	exists, err := s.inboxHasAfter(ctx, hiveID, to, afterID)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	listener := s.inboxListener()
	if err = listener.waitReady(ctx); err != nil {
		return err
	}
	waiter := &inboxWaiter{hiveID: hiveID, to: to, wake: make(chan struct{}, 1)}
	listener.add(waiter)
	defer listener.remove(waiter)
	// The shared listener is active before the waiter is registered, so a
	// message committed after this re-check must produce a notification.
	if exists, err = s.inboxHasAfter(ctx, hiveID, to, afterID); err != nil {
		return err
	} else if exists {
		return nil
	}
	ticker := time.NewTicker(inboxRecheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-waiter.wake:
		case <-ticker.C:
		}
		exists, err = s.inboxHasAfter(ctx, hiveID, to, afterID)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
}

// DeliverMessage is what the River delivery job runs: it flips a pending
// message to delivered and wakes waiters. The actual push into a CLI session
// is the next round's Cell runtime; it installs Store.MessageSink, which is
// called after the state change commits.
func (s *Store) DeliverMessage(ctx context.Context, id string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	m, err := scanMessage(tx.QueryRow(ctx, `SELECT `+messageColumns+` FROM messages WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return err
	}
	if m.State == domain.MessagePending {
		if _, err = tx.Exec(ctx, `UPDATE messages SET state='delivered' WHERE id=$1`, id); err != nil {
			return err
		}
		m.State = domain.MessageDelivered
	}
	if _, err = tx.Exec(ctx, `SELECT pg_notify($1,$2)`, inboxChannel, notifyPayload(m.HiveID, m.To)); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if s.MessageSink != nil {
		return s.MessageSink(ctx, m)
	}
	return nil
}
