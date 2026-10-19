-- Round 2: persist the CLI session used to resume a Forager, and mint one MCP
-- token per Agent. The columns are added in place so version-5 data stays
-- readable; existing rows get an empty session and no Agent token.

ALTER TABLE agents ADD COLUMN IF NOT EXISTS cli_session_ref text NOT NULL DEFAULT '';

ALTER TABLE mcp_tokens ADD COLUMN IF NOT EXISTS agent_id text REFERENCES agents(id) ON DELETE CASCADE;
ALTER TABLE mcp_tokens ADD COLUMN IF NOT EXISTS revoked_at timestamptz;
CREATE INDEX IF NOT EXISTS mcp_tokens_by_agent ON mcp_tokens(agent_id) WHERE agent_id IS NOT NULL;
