-- Do not backfill: historical Agents have no reliable startup version.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS methodology_version_id TEXT REFERENCES methodologies(id);
