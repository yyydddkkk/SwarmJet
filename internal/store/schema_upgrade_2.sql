CREATE TABLE IF NOT EXISTS platform_upgrades (
 id text PRIMARY KEY,
 status text NOT NULL,
 active boolean NOT NULL DEFAULT true,
 maintenance boolean NOT NULL DEFAULT false,
 body jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS protocol_version integer NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX IF NOT EXISTS one_active_platform_upgrade ON platform_upgrades(active) WHERE active;
CREATE INDEX IF NOT EXISTS platform_upgrades_recent ON platform_upgrades(created_at DESC);

CREATE OR REPLACE FUNCTION reject_task_during_platform_upgrade() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS (SELECT 1 FROM platform_upgrades WHERE maintenance) THEN
  RAISE EXCEPTION 'platform upgrade maintenance' USING ERRCODE='check_violation';
 END IF;
 RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS platform_upgrade_task_gate ON tasks;
CREATE TRIGGER platform_upgrade_task_gate BEFORE INSERT ON tasks
 FOR EACH ROW EXECUTE FUNCTION reject_task_during_platform_upgrade();
