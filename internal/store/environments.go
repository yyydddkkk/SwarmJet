package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"swarmjet/internal/domain"
)

func (s *Store) NetworkProfiles(ctx context.Context) ([]domain.NetworkProfile, error) {
	rows, e := s.Pool.Query(ctx, `SELECT DISTINCT ON(id) body FROM network_profiles ORDER BY id,version DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.NetworkProfile{}
	for rows.Next() {
		var b []byte
		var p domain.NetworkProfile
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &p); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) NetworkProfile(ctx context.Context, ref domain.NetworkVersion) (domain.NetworkProfile, string, error) {
	var b []byte
	var secret string
	e := s.Pool.QueryRow(ctx, `SELECT body,secret FROM network_profiles WHERE id=$1 AND ($2=0 OR version=$2) ORDER BY version DESC LIMIT 1`, ref.ID, ref.Version).Scan(&b, &secret)
	var p domain.NetworkProfile
	if e == nil {
		e = json.Unmarshal(b, &p)
	}
	return p, secret, mapErr(e)
}
func (s *Store) SaveNetwork(ctx context.Context, p domain.NetworkProfile, secret string) (domain.NetworkProfile, error) {
	if e := p.Validate(); e != nil {
		return p, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return p, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(84261303)`); e != nil {
		return p, e
	}
	var v int
	if e = tx.QueryRow(ctx, `SELECT COALESCE(MAX(version),0) FROM network_profiles WHERE id=$1`, p.ID).Scan(&v); e != nil {
		return p, e
	}
	if v != p.Version {
		return p, ErrConflict
	}
	p.Version++
	b, e := json.Marshal(p)
	if e != nil {
		return p, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO network_profiles(id,version,body,secret) VALUES($1,$2,$3,$4)`, p.ID, p.Version, b, secret)
	if e != nil {
		return p, e
	}
	return p, tx.Commit(ctx)
}
func (s *Store) Setting(ctx context.Context, name string, out any) error {
	var b []byte
	e := s.Pool.QueryRow(ctx, `SELECT body FROM platform_settings WHERE name=$1`, name).Scan(&b)
	if e != nil {
		return mapErr(e)
	}
	return json.Unmarshal(b, out)
}
func (s *Store) SetSetting(ctx context.Context, name string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.Pool.Exec(ctx, `INSERT INTO platform_settings(name,body) VALUES($1,$2) ON CONFLICT(name) DO UPDATE SET body=excluded.body`, name, b)
	return e
}
func (s *Store) ResolveNetwork(ctx context.Context, c domain.NetworkChoice) (domain.NetworkVersion, error) {
	if e := c.Validate(); e != nil {
		return domain.NetworkVersion{}, e
	}
	if c.Mode == "direct" {
		return domain.NetworkVersion{}, nil
	}
	if c.Mode == "" || c.Mode == "default" {
		if e := s.Setting(ctx, "default_network", &c.ID); e != nil && !errors.Is(e, ErrNotFound) {
			return domain.NetworkVersion{}, e
		}
	}
	if c.ID == "" {
		return domain.NetworkVersion{}, nil
	}
	p, _, e := s.NetworkProfile(ctx, domain.NetworkVersion{ID: c.ID})
	if e == nil && p.Revoked {
		e = fmt.Errorf("网络配置已撤销")
	}
	return domain.NetworkVersion{ID: p.ID, Version: p.Version}, e
}
func (s *Store) NodeConfig(ctx context.Context, id string) (domain.NodeConfig, error) {
	var b []byte
	var c domain.NodeConfig
	e := s.Pool.QueryRow(ctx, `SELECT body FROM node_configs WHERE id=$1`, id).Scan(&b)
	if e == nil {
		e = json.Unmarshal(b, &c)
	}
	return c, mapErr(e)
}
func (s *Store) SaveNodeConfig(ctx context.Context, id string, c domain.NodeConfig) (domain.NodeConfig, error) {
	if e := c.Validate(); e != nil {
		return c, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return c, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(84261302)`); e != nil {
		return c, e
	}
	var old int
	e = tx.QueryRow(ctx, `SELECT version FROM node_configs WHERE id=$1 FOR UPDATE`, id).Scan(&old)
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return c, e
	}
	if old != c.Version {
		return c, ErrConflict
	}
	if c.Archived {
		var active bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tasks WHERE node_id=$1 AND NOT terminal UNION ALL SELECT 1 FROM initializations WHERE body->>'nodeId'=$1 AND status NOT IN ('ready','cancelled'))`, id).Scan(&active)
		if e != nil {
			return c, e
		}
		if active {
			return c, fmt.Errorf("%w: 节点仍有未结束任务，不能归档", ErrConflict)
		}
		c.Enabled = false
	}
	c.Version++
	b, e := json.Marshal(c)
	if e != nil {
		return c, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO node_configs(id,version,body) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET version=excluded.version,body=excluded.body`, id, c.Version, b)
	if e != nil {
		return c, e
	}
	return c, tx.Commit(ctx)
}
func (s *Store) NodeCredential(ctx context.Context, id string) (string, bool, error) {
	var secret string
	var revoked bool
	e := s.Pool.QueryRow(ctx, `SELECT secret,revoked FROM node_credentials WHERE id=$1`, id).Scan(&secret, &revoked)
	return secret, revoked, mapErr(e)
}
func (s *Store) SaveNodeCredential(ctx context.Context, id, secret string, revoked bool) error {
	_, e := s.Pool.Exec(ctx, `INSERT INTO node_credentials(id,secret,revoked) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET secret=excluded.secret,revoked=excluded.revoked`, id, secret, revoked)
	return e
}

// pickTaskNode runs under the same allocation lock as configuration updates.
// Unmanaged legacy nodes retain old semantics until explicitly configured.
func pickTaskNode(ctx context.Context, tx pgx.Tx, t *domain.Task) (string, bool, error) {
	rows, e := tx.Query(ctx, `SELECT n.id,n.readiness,c.body,COALESCE(r.running,0),n.capacity
 FROM nodes n LEFT JOIN node_configs c ON c.id=n.id
 LEFT JOIN (SELECT node_id,count(*) running FROM (
 SELECT node_id FROM tasks WHERE NOT terminal AND node_id IS NOT NULL
 UNION ALL SELECT body->>'nodeId' FROM initializations WHERE status NOT IN ('ready','cancelled') AND COALESCE(body->>'nodeId','')<>''
 ) work GROUP BY node_id) r ON r.node_id=n.id
 WHERE NOT EXISTS(SELECT 1 FROM node_credentials nc WHERE nc.id=n.id AND nc.revoked) AND n.last_seen > now() - interval '30 seconds' ORDER BY COALESCE(r.running,0),n.id`)
	if e != nil {
		return "", false, e
	}
	defer rows.Close()
	type candidate struct {
		id                string
		ready, config     []byte
		running, capacity int
	}
	all := []candidate{}
	for rows.Next() {
		var c candidate
		if e = rows.Scan(&c.id, &c.ready, &c.config, &c.running, &c.capacity); e != nil {
			return "", false, e
		}
		all = append(all, c)
	}
	if e = rows.Err(); e != nil {
		return "", false, e
	}
	rows.Close()
	reasons := []string{}
	for _, n := range all {
		if !t.Project.Nodes.Allows(n.id) {
			reasons = append(reasons, n.id+": 不在项目节点范围")
			continue
		}
		if len(n.config) == 0 {
			var readiness domain.NodeReadiness
			_ = json.Unmarshal(n.ready, &readiness)
			if readiness.Status != "" {
				continue
			}
			if n.running < n.capacity {
				return n.id, true, nil
			}
			continue
		}
		var cfg domain.NodeConfig
		var ready domain.NodeReadiness
		if e = json.Unmarshal(n.config, &cfg); e != nil {
			return "", false, e
		}
		if e = json.Unmarshal(n.ready, &ready); e != nil {
			return "", false, e
		}
		if !cfg.Enabled || cfg.Archived {
			reasons = append(reasons, n.id+": 已暂停或归档")
			continue
		}
		if n.running >= cfg.Capacity {
			reasons = append(reasons, n.id+": 容量已满")
			continue
		}
		if ready.Status != "ready" || ready.ConfigVersion != cfg.Version {
			reasons = append(reasons, n.id+": 镜像或配置未就绪")
			continue
		}
		ref := domain.NetworkVersion{}
		choice := cfg.Network
		if choice.Mode != "direct" {
			if choice.Mode == "" || choice.Mode == "default" {
				var b []byte
				e = tx.QueryRow(ctx, `SELECT body FROM platform_settings WHERE name='default_network'`).Scan(&b)
				if e != nil && !errors.Is(e, pgx.ErrNoRows) {
					return "", false, e
				}
				if e == nil {
					if e = json.Unmarshal(b, &choice.ID); e != nil {
						return "", false, e
					}
				}
			}
			if choice.ID != "" {
				var b []byte
				e = tx.QueryRow(ctx, `SELECT body FROM network_profiles WHERE id=$1 ORDER BY version DESC LIMIT 1`, choice.ID).Scan(&b)
				if errors.Is(e, pgx.ErrNoRows) {
					continue
				}
				if e != nil {
					return "", false, e
				}
				var p domain.NetworkProfile
				if e = json.Unmarshal(b, &p); e != nil {
					return "", false, e
				}
				if p.Revoked {
					continue
				}
				ref = domain.NetworkVersion{ID: p.ID, Version: p.Version}
			}
		}
		if ref != ready.Network {
			reasons = append(reasons, n.id+": 网络配置待同步")
			continue
		}
		p := t.Project
		ok := true
		for _, agent := range []*domain.AgentConfig{&p.Development, &p.Review} {
			agent.DockerNetwork = "sj-node-" + n.id
			agent.MemoryMB = cfg.MemoryMB
			if agent.MemoryMB == 0 {
				agent.MemoryMB = 2048
			}
			agent.CPUs = cfg.CPUs
			if agent.CPUs == 0 {
				agent.CPUs = 2
			}
			agent.PidsLimit = cfg.PidsLimit
			if agent.PidsLimit == 0 {
				agent.PidsLimit = 256
			}
			if agent.Image == "" {
				agent.Image = cfg.Image
			}
			imageAgents := []string{}
			if len(ready.Images) == 0 && agent.Image == ready.Image {
				imageAgents = ready.Agents
			}
			for _, image := range ready.Images {
				if image.Reference == agent.Image && image.Status == "ready" {
					imageAgents = image.Agents
				}
			}
			if !domain.RepositoryDigest.MatchString(agent.Image) {
				ok = false
				break
			}
			supported := false
			for _, id := range imageAgents {
				if id == agent.Adapter {
					supported = true
				}
			}
			if !supported {
				ok = false
				break
			}
		}
		if !ok {
			reasons = append(reasons, n.id+": 镜像或 Agent 能力不匹配")
			continue
		}
		t.Project = p
		t.Network = ref
		t.NodeConfigVersion = cfg.Version
		return n.id, true, nil
	}
	if len(reasons) > 20 {
		reasons = reasons[:20]
	}
	t.Message = strings.Join(reasons, "；")
	if t.Message == "" {
		t.Message = "没有在线且可用的节点，请检查注册、心跳和凭据"
	}
	return "", false, nil
}

func (s *Store) AssignInitializationNode(ctx context.Context, in domain.Initialization, p domain.Project) (domain.Initialization, error) {
	if in.NodeID != "" {
		return in, nil
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return in, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(84261302)`); e != nil {
		return in, e
	}
	p.Development.Adapter = "opencode"
	p.Review = p.Development
	task := domain.Task{Project: p}
	id, found, e := pickTaskNode(ctx, tx, &task)
	if e != nil {
		return in, e
	}
	if !found {
		return in, fmt.Errorf("%w: 初始化需要具备 OpenCode 的就绪节点", ErrNoNode)
	}
	in.ExecutionConfig = task.Project.Development
	in.NodeID = id
	in.Image = task.Project.Development.Image
	in.Network = task.Network
	in.NodeConfigVersion = task.NodeConfigVersion
	b, e := json.Marshal(in)
	if e != nil {
		return in, e
	}
	_, e = tx.Exec(ctx, `UPDATE initializations SET body=$2,updated_at=now() WHERE id=$1`, in.ID, b)
	if e != nil {
		return in, e
	}
	return in, tx.Commit(ctx)
}
