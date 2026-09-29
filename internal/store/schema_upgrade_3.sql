-- Version 2 may have been applied before protocol_version was added to its SQL.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS protocol_version integer NOT NULL DEFAULT 0;
