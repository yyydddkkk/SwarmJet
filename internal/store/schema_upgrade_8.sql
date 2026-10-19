-- Round 4: independent verification. An Agent is either the worker that owns a
-- task or a read-only verifier. A verdict records one verifier's conclusion
-- about one PR head commit; the unique key makes RecordVerdict idempotent.

ALTER TABLE agents ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'worker';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS verify_sha text NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS owner_id text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS verdicts(
  id text PRIMARY KEY,
  hive_id text NOT NULL REFERENCES hives(id) ON DELETE CASCADE,
  pr_number bigint NOT NULL,
  head_sha text NOT NULL,
  owner_id text NOT NULL DEFAULT '',
  verifier_id text NOT NULL,
  result text NOT NULL,
  summary text NOT NULL DEFAULT '',
  findings jsonb,
  comment_url text NOT NULL DEFAULT '',
  superseded_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (hive_id, pr_number, head_sha, verifier_id)
);
CREATE INDEX IF NOT EXISTS verdicts_by_pr ON verdicts(hive_id, pr_number, created_at DESC);
CREATE INDEX IF NOT EXISTS verdicts_by_owner_result ON verdicts(hive_id, owner_id, result);
CREATE INDEX IF NOT EXISTS verdicts_by_verifier ON verdicts(verifier_id);
