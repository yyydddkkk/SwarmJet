-- Round 4 D: individual capabilities. Agents carry the methodology role that
-- spawned them, an optional pinned revision and the Agent that spawned them.
-- Messages carry provenance so a prompt can tell the user's words from another
-- Agent's and from third-party text.

ALTER TABLE agents ADD COLUMN IF NOT EXISTS role_name text NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS revision text NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS parent_id text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS agents_by_parent ON agents(parent_id) WHERE parent_id <> '';

ALTER TABLE messages ADD COLUMN IF NOT EXISTS trust text NOT NULL DEFAULT 'agent';
