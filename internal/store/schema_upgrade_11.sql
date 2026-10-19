-- Round 4 task B: model usage attributed to the Agent whose platform token
-- authorized the gateway request. Costs are estimated from the built-in price
-- table; an unknown model is recorded with cost 0 and unknown_price=true.

CREATE TABLE IF NOT EXISTS llm_usage(
  id bigserial PRIMARY KEY,
  agent_id text NOT NULL,
  hive_id text NOT NULL,
  model text NOT NULL DEFAULT '',
  input_tokens bigint NOT NULL DEFAULT 0,
  output_tokens bigint NOT NULL DEFAULT 0,
  cache_read_tokens bigint NOT NULL DEFAULT 0,
  cache_write_tokens bigint NOT NULL DEFAULT 0,
  cost_usd double precision NOT NULL DEFAULT 0,
  unknown_price boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS llm_usage_agent_created ON llm_usage(agent_id, created_at DESC);
CREATE INDEX IF NOT EXISTS llm_usage_hive_created ON llm_usage(hive_id, created_at DESC);
