-- Hive / Queen / Drone data layer (ADR docs/claude/adr-hive-queen-drone.md §16).
-- Old pipeline tables stay untouched; this migration only adds the new tables.

CREATE TABLE IF NOT EXISTS hives(
  id text PRIMARY KEY,
  name text NOT NULL,
  repository text NOT NULL UNIQUE,
  queen_enabled boolean NOT NULL DEFAULT false,
  config jsonb NOT NULL DEFAULT '{}'::jsonb,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Queen and Drone share this table; role separates them. Every Hive has one
-- Queen record whose state is disabled until the user enables it.
CREATE TABLE IF NOT EXISTS agents(
  id text PRIMARY KEY,
  hive_id text NOT NULL REFERENCES hives(id) ON DELETE CASCADE,
  role text NOT NULL CHECK (role IN ('queen','drone')),
  state text NOT NULL,
  preset text NOT NULL DEFAULT '',
  cli text NOT NULL DEFAULT '',
  task text NOT NULL DEFAULT '',
  branch text NOT NULL DEFAULT '',
  issue_number bigint NOT NULL DEFAULT 0,
  pr_number bigint NOT NULL DEFAULT 0,
  node_id text NOT NULL DEFAULT '',
  chain_id text NOT NULL DEFAULT '',
  handoff jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS agents_one_queen ON agents(hive_id) WHERE role='queen';
CREATE INDEX IF NOT EXISTS agents_by_hive_state ON agents(hive_id,state);
CREATE INDEX IF NOT EXISTS agents_by_hive_issue ON agents(hive_id,issue_number);
CREATE INDEX IF NOT EXISTS agents_by_hive_pr ON agents(hive_id,pr_number);
CREATE INDEX IF NOT EXISTS agents_by_hive_branch ON agents(hive_id,branch);
CREATE INDEX IF NOT EXISTS agents_by_hive_chain ON agents(hive_id,chain_id);

-- Inbox messages. state is pending/delivered/handled; claimed_by plus
-- claim_expires_at implement the Hive Inbox claim lease.
CREATE TABLE IF NOT EXISTS messages(
  id text PRIMARY KEY,
  hive_id text NOT NULL REFERENCES hives(id) ON DELETE CASCADE,
  from_addr text NOT NULL,
  to_addr text NOT NULL,
  kind text NOT NULL,
  chain_id text NOT NULL DEFAULT '',
  hop integer NOT NULL DEFAULT 0,
  body text NOT NULL DEFAULT '',
  artifacts jsonb,
  state text NOT NULL DEFAULT 'pending',
  claimed_by text NOT NULL DEFAULT '',
  claim_expires_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS messages_by_inbox ON messages(hive_id,to_addr,id);

-- MCP access tokens. Only the hash is stored; the plaintext is returned once.
CREATE TABLE IF NOT EXISTS mcp_tokens(
  id text PRIMARY KEY,
  name text NOT NULL,
  scope text NOT NULL,
  token_hash text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now()
);
