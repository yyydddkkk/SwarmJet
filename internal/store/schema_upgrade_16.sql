CREATE TABLE IF NOT EXISTS pr_snapshots (
  hive_id text NOT NULL REFERENCES hives(id) ON DELETE CASCADE,
  number bigint NOT NULL,
  title text NOT NULL DEFAULT '',
  state text NOT NULL DEFAULT '',
  merged boolean NOT NULL DEFAULT false,
  closed_at timestamptz,
  url text NOT NULL DEFAULT '',
  head_sha text NOT NULL DEFAULT '',
  updated_at timestamptz,
  ready boolean NOT NULL DEFAULT false,
  reasons jsonb NOT NULL DEFAULT '[]'::jsonb,
  observed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (hive_id, number)
);
