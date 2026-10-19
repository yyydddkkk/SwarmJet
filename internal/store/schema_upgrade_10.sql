-- Round 4 task E: attestations. One Agent's conclusion about one PR head
-- commit. The unique key makes a retried attest idempotent; superseded_at
-- records that a newer head made an older conclusion obsolete.

CREATE TABLE IF NOT EXISTS attestations(
  id text PRIMARY KEY,
  hive_id text NOT NULL REFERENCES hives(id) ON DELETE CASCADE,
  pr_number bigint NOT NULL,
  head_sha text NOT NULL,
  attester_id text NOT NULL,
  author_id text NOT NULL DEFAULT '',
  is_author boolean NOT NULL DEFAULT false,
  result text NOT NULL,
  summary text NOT NULL DEFAULT '',
  findings jsonb,
  comment_url text NOT NULL DEFAULT '',
  superseded_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (hive_id, pr_number, head_sha, attester_id)
);

CREATE INDEX IF NOT EXISTS attestations_by_pr ON attestations(hive_id, pr_number, created_at DESC);
CREATE INDEX IF NOT EXISTS attestations_by_attester ON attestations(attester_id);
