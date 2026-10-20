-- Durable requests waiting for a Hive concurrency slot. The Agent ID is the
-- idempotency key; completed rows remain to reject late duplicate webhooks.
CREATE TABLE IF NOT EXISTS spawn_queue (
  seq bigserial PRIMARY KEY,
  hive_id text NOT NULL REFERENCES hives(id) ON DELETE CASCADE,
  agent_id text NOT NULL UNIQUE,
  agent jsonb NOT NULL,
  task_message jsonb NOT NULL,
  limits jsonb NOT NULL,
  state text NOT NULL DEFAULT 'waiting',
  created_at timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE INDEX IF NOT EXISTS spawn_queue_waiting ON spawn_queue(hive_id, seq) WHERE state='waiting';
