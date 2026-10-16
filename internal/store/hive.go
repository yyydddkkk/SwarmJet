package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/yyydddkkk/SwarmJet/internal/domain"
)

// The Hive data layer lives in this file and its siblings (message.go,
// token.go). It is deliberately separate from the legacy pipeline tables: new
// methods return domain errors, while the old ErrNotFound/ErrConflict above
// stay untouched for the existing code.

const hiveColumns = `id,name,repository,queen_enabled,config,version,created_at,updated_at`

const agentColumns = `id,hive_id,role,state,preset,cli,task,branch,issue_number,pr_number,node_id,chain_id,handoff,created_at,updated_at`

// newOpaqueID returns 128 random bits as hex. It is used for Hive, Agent and
// Token identifiers. Message IDs use newMessageID instead so that they sort in
// insertion order.
func newOpaqueID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// domainErr translates pgx errors to the domain contract: ErrNoRows becomes
// domain.ErrNotFound, unique violations become domain.ErrConflict.
func domainErr(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var p *pgconn.PgError
	if errors.As(e, &p) && p.Code == "23505" {
		return domain.ErrConflict
	}
	return e
}

// applyHiveDefaults fills the zero fields of a HiveConfig with the documented
// defaults. Reads go through here, so callers never see a zero limit.
func applyHiveDefaults(c *domain.HiveConfig) {
	if c.CLI == "" {
		c.CLI = "opencode"
	}
	if c.MaxConcurrentDrones == 0 {
		c.MaxConcurrentDrones = 4
	}
	if c.MaxHops == 0 {
		c.MaxHops = 10
	}
	if c.MaxDronesPerChain == 0 {
		c.MaxDronesPerChain = 10
	}
	if c.IdleParkMinutes == 0 {
		c.IdleParkMinutes = 30
	}
	if c.HibernateHours == 0 {
		c.HibernateHours = 24
	}
	if c.TurnTimeoutMinutes == 0 {
		c.TurnTimeoutMinutes = 120
	}
	if c.DroneMaxLifetimeHours == 0 {
		c.DroneMaxLifetimeHours = 168
	}
}

func scanHive(row pgx.Row) (domain.Hive, error) {
	var h domain.Hive
	var config []byte
	err := row.Scan(&h.ID, &h.Name, &h.Repository, &h.QueenEnabled, &config, &h.Version, &h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		return domain.Hive{}, domainErr(err)
	}
	if len(config) > 0 {
		if err = json.Unmarshal(config, &h.Config); err != nil {
			return domain.Hive{}, err
		}
	}
	applyHiveDefaults(&h.Config)
	return h, nil
}

// CreateHive inserts a Hive and its disabled Queen in one transaction. The
// stored config keeps the caller's zero values; reads apply defaults.
func (s *Store) CreateHive(ctx context.Context, h domain.Hive) (domain.Hive, error) {
	now := time.Now().UTC()
	if h.ID == "" {
		id, err := newOpaqueID()
		if err != nil {
			return domain.Hive{}, err
		}
		h.ID = id
	}
	if h.Version == 0 {
		h.Version = 1
	}
	if h.CreatedAt.IsZero() {
		h.CreatedAt = now
	}
	h.UpdatedAt = now
	config, err := json.Marshal(h.Config)
	if err != nil {
		return domain.Hive{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.Hive{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO hives(id,name,repository,queen_enabled,config,version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		h.ID, h.Name, h.Repository, h.QueenEnabled, config, h.Version, h.CreatedAt, h.UpdatedAt); err != nil {
		return domain.Hive{}, domainErr(err)
	}
	queenID, err := newOpaqueID()
	if err != nil {
		return domain.Hive{}, err
	}
	cli := h.Config.CLI
	if cli == "" {
		cli = "opencode"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agents(id,hive_id,role,state,cli,created_at,updated_at) VALUES($1,$2,'queen','disabled',$3,$4,$4)`,
		queenID, h.ID, cli, now); err != nil {
		return domain.Hive{}, domainErr(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Hive{}, err
	}
	applyHiveDefaults(&h.Config)
	return h, nil
}

func (s *Store) Hive(ctx context.Context, id string) (domain.Hive, error) {
	return scanHive(s.Pool.QueryRow(ctx, `SELECT `+hiveColumns+` FROM hives WHERE id=$1`, id))
}

func (s *Store) HiveByRepository(ctx context.Context, repository string) (domain.Hive, error) {
	return scanHive(s.Pool.QueryRow(ctx, `SELECT `+hiveColumns+` FROM hives WHERE repository=$1`, repository))
}

func (s *Store) Hives(ctx context.Context) ([]domain.Hive, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+hiveColumns+` FROM hives ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hives []domain.Hive
	for rows.Next() {
		h, err := scanHive(rows)
		if err != nil {
			return nil, err
		}
		hives = append(hives, h)
	}
	return hives, rows.Err()
}

// UpdateHive applies the caller's version as an optimistic lock. A stale
// version yields domain.ErrConflict; a missing Hive yields domain.ErrNotFound.
func (s *Store) UpdateHive(ctx context.Context, h domain.Hive) (domain.Hive, error) {
	now := time.Now().UTC()
	config, err := json.Marshal(h.Config)
	if err != nil {
		return domain.Hive{}, err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE hives SET name=$3,repository=$4,queen_enabled=$5,config=$6,version=version+1,updated_at=$7 WHERE id=$1 AND version=$2`,
		h.ID, h.Version, h.Name, h.Repository, h.QueenEnabled, config, now)
	if err != nil {
		return domain.Hive{}, domainErr(err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hives WHERE id=$1)`, h.ID).Scan(&exists); err != nil {
			return domain.Hive{}, err
		}
		if !exists {
			return domain.Hive{}, domain.ErrNotFound
		}
		return domain.Hive{}, domain.ErrConflict
	}
	h.Version++
	h.UpdatedAt = now
	applyHiveDefaults(&h.Config)
	return h, nil
}

func scanAgent(row pgx.Row) (domain.Agent, error) {
	var a domain.Agent
	var role, state string
	var handoff []byte
	err := row.Scan(&a.ID, &a.HiveID, &role, &state, &a.Preset, &a.CLI, &a.Task, &a.Branch,
		&a.IssueNumber, &a.PRNumber, &a.NodeID, &a.ChainID, &handoff, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return domain.Agent{}, domainErr(err)
	}
	a.Role, a.State = domain.AgentRole(role), domain.AgentState(state)
	if len(handoff) > 0 {
		var h domain.Handoff
		if err = json.Unmarshal(handoff, &h); err != nil {
			return domain.Agent{}, err
		}
		a.Handoff = &h
	}
	return a, nil
}

// CreateDrone inserts a Drone after checking the Hive's concurrency and chain
// limits. It locks the Hive row so concurrent creators cannot overshoot.
func (s *Store) CreateDrone(ctx context.Context, a domain.Agent) (domain.Agent, error) {
	now := time.Now().UTC()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return domain.Agent{}, err
	}
	defer tx.Rollback(ctx)
	var config []byte
	if err = tx.QueryRow(ctx, `SELECT config FROM hives WHERE id=$1 FOR UPDATE`, a.HiveID).Scan(&config); err != nil {
		return domain.Agent{}, domainErr(err)
	}
	cfg := domain.HiveConfig{}
	if len(config) > 0 {
		if err = json.Unmarshal(config, &cfg); err != nil {
			return domain.Agent{}, err
		}
	}
	applyHiveDefaults(&cfg)
	var active int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agents WHERE hive_id=$1 AND role='drone' AND state IN ('creating','active','parked')`, a.HiveID).Scan(&active); err != nil {
		return domain.Agent{}, err
	}
	if active >= cfg.MaxConcurrentDrones {
		return domain.Agent{}, domain.ErrHiveLimit
	}
	if a.ChainID != "" {
		var chained int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM agents WHERE hive_id=$1 AND chain_id=$2`, a.HiveID, a.ChainID).Scan(&chained); err != nil {
			return domain.Agent{}, err
		}
		if chained >= cfg.MaxDronesPerChain {
			return domain.Agent{}, domain.ErrChainLimit
		}
	}
	if a.ID == "" {
		id, e := newOpaqueID()
		if e != nil {
			return domain.Agent{}, e
		}
		a.ID = id
	}
	if a.Role == "" {
		a.Role = domain.RoleDrone
	}
	if a.State == "" {
		a.State = domain.AgentCreating
	}
	if a.CLI == "" {
		a.CLI = cfg.CLI
	}
	if a.Branch == "" {
		a.Branch = "swarm/" + a.HiveID + "/" + a.ID
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	var handoff []byte
	if a.Handoff != nil {
		if handoff, err = json.Marshal(a.Handoff); err != nil {
			return domain.Agent{}, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO agents(`+agentColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		a.ID, a.HiveID, string(a.Role), string(a.State), a.Preset, a.CLI, a.Task, a.Branch,
		a.IssueNumber, a.PRNumber, a.NodeID, a.ChainID, handoff, a.CreatedAt, a.UpdatedAt); err != nil {
		return domain.Agent{}, domainErr(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Agent{}, err
	}
	return a, nil
}

func (s *Store) Agent(ctx context.Context, id string) (domain.Agent, error) {
	return scanAgent(s.Pool.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE id=$1`, id))
}

func (s *Store) Queen(ctx context.Context, hiveID string) (domain.Agent, error) {
	return scanAgent(s.Pool.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE hive_id=$1 AND role='queen'`, hiveID))
}

// Drones returns a Hive's Drones, optionally filtered by state. The Queen is
// never included.
func (s *Store) Drones(ctx context.Context, hiveID string, states ...domain.AgentState) ([]domain.Agent, error) {
	query := `SELECT ` + agentColumns + ` FROM agents WHERE hive_id=$1 AND role='drone'`
	args := []any{hiveID}
	if len(states) > 0 {
		values := make([]string, len(states))
		for i, state := range states {
			values[i] = string(state)
		}
		query += ` AND state = ANY($2)`
		args = append(args, values)
	}
	query += ` ORDER BY created_at,id`
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var drones []domain.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		drones = append(drones, a)
	}
	return drones, rows.Err()
}

// liveDrone returns the newest non-terminal Drone matching a predicate. Events
// for finished Drones fall through to the Hive Inbox instead of a dead Agent.
const liveDrone = ` AND state NOT IN ('completed','failed','cancelled')`

func (s *Store) DroneByIssue(ctx context.Context, hiveID string, issue int64) (domain.Agent, error) {
	return scanAgent(s.Pool.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE hive_id=$1 AND role='drone' AND issue_number=$2`+liveDrone+` ORDER BY created_at DESC,id DESC LIMIT 1`, hiveID, issue))
}

func (s *Store) DroneByPR(ctx context.Context, hiveID string, pr int64) (domain.Agent, error) {
	return scanAgent(s.Pool.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE hive_id=$1 AND role='drone' AND pr_number=$2`+liveDrone+` ORDER BY created_at DESC,id DESC LIMIT 1`, hiveID, pr))
}

func (s *Store) DroneByBranch(ctx context.Context, hiveID, branch string) (domain.Agent, error) {
	return scanAgent(s.Pool.QueryRow(ctx, `SELECT `+agentColumns+` FROM agents WHERE hive_id=$1 AND role='drone' AND branch=$2`+liveDrone+` ORDER BY created_at DESC,id DESC LIMIT 1`, hiveID, branch))
}

// TransitionAgent is a compare-and-swap on the Agent state.
func (s *Store) TransitionAgent(ctx context.Context, id string, from, to domain.AgentState) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE agents SET state=$3,updated_at=now() WHERE id=$1 AND state=$2`, id, string(from), string(to))
	if err != nil {
		return domainErr(err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=$1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return domain.ErrNotFound
		}
		return domain.ErrConflict
	}
	return nil
}

func (s *Store) UpdateHandoff(ctx context.Context, id string, h domain.Handoff) error {
	body, err := json.Marshal(h)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE agents SET handoff=$2,updated_at=now() WHERE id=$1`, id, body)
	if err != nil {
		return domainErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
