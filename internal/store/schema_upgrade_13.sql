-- Track activations independently of immutable version contents. Historical
-- events seed the activation order when upgrading an existing installation.
ALTER TABLE methodologies ADD COLUMN IF NOT EXISTS activated_at timestamptz;
ALTER TABLE methodologies ADD COLUMN IF NOT EXISTS consecutive_failures integer NOT NULL DEFAULT 0;
ALTER TABLE methodologies ADD COLUMN IF NOT EXISTS auto_rolled_back_at timestamptz;
UPDATE methodologies m SET activated_at=COALESCE(
  (SELECT max(e.created_at) FROM methodology_events e WHERE e.hive_id=m.hive_id AND e.version=m.version),
  m.created_at) WHERE activated_at IS NULL;

CREATE OR REPLACE FUNCTION methodology_activation_tracking() RETURNS trigger AS $$
BEGIN
  IF NEW.active AND (TG_OP='INSERT' OR NOT OLD.active) THEN
    NEW.activated_at := clock_timestamp();
    NEW.consecutive_failures := 0;
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS methodology_activation_tracking ON methodologies;
CREATE TRIGGER methodology_activation_tracking BEFORE INSERT OR UPDATE OF active ON methodologies
  FOR EACH ROW EXECUTE FUNCTION methodology_activation_tracking();
