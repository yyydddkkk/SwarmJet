-- Round 4: a Hive's methodology is a versioned shared asset. Every publish
-- creates an immutable version; activating a version (including an older one,
-- for rollback) moves the single active flag. History is append-only.

CREATE TABLE IF NOT EXISTS methodologies(
  id text PRIMARY KEY,
  hive_id text NOT NULL REFERENCES hives(id) ON DELETE CASCADE,
  version integer NOT NULL CHECK (version > 0),
  active boolean NOT NULL DEFAULT false,
  source jsonb,
  manifest jsonb NOT NULL,
  files jsonb NOT NULL DEFAULT '[]'::jsonb,
  digest text NOT NULL,
  note text NOT NULL DEFAULT '',
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(hive_id, version)
);

-- At most one active version per Hive. The activation transaction clears the
-- previous flag before setting the new one, so the index never sees two.
CREATE UNIQUE INDEX IF NOT EXISTS one_active_methodology ON methodologies(hive_id) WHERE active;

CREATE TABLE IF NOT EXISTS methodology_events(
  id bigserial PRIMARY KEY,
  hive_id text NOT NULL REFERENCES hives(id) ON DELETE CASCADE,
  action text NOT NULL CHECK (action IN ('publish','activate')),
  version integer NOT NULL CHECK (version > 0),
  note text NOT NULL DEFAULT '',
  actor text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS methodology_events_by_hive ON methodology_events(hive_id, id);
