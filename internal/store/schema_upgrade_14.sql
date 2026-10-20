-- Small invalidations only; committed entities are hydrated by the API.
CREATE OR REPLACE FUNCTION swarmjet_notify_event() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE event_type text := TG_ARGV[0]; entity_id text; hive text;
BEGIN
  IF TG_TABLE_NAME = 'agents' AND TG_OP = 'UPDATE' THEN
    IF ROW(OLD.state,OLD.pr_number,OLD.issue_number,OLD.role_name) IS NOT DISTINCT FROM
       ROW(NEW.state,NEW.pr_number,NEW.issue_number,NEW.role_name) THEN RETURN NEW; END IF;
  END IF;
  IF TG_TABLE_NAME = 'messages' AND TG_OP <> 'INSERT' THEN RETURN NEW; END IF;
  IF TG_TABLE_NAME = 'hives' THEN hive := NEW.id; entity_id := NEW.id;
  ELSIF TG_TABLE_NAME = 'attestations' THEN hive := NEW.hive_id; entity_id := NEW.pr_number::text;
  ELSIF TG_TABLE_NAME = 'spawn_queue' THEN hive := NEW.hive_id; entity_id := NEW.agent_id;
  ELSE hive := NEW.hive_id; entity_id := NEW.id::text; END IF;
  PERFORM pg_notify('swarmjet_events', json_build_object('type',event_type,'hiveId',hive,'id',entity_id)::text);
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS swarmjet_events ON messages;
CREATE TRIGGER swarmjet_events AFTER INSERT OR UPDATE ON messages FOR EACH ROW EXECUTE FUNCTION swarmjet_notify_event('message');
DROP TRIGGER IF EXISTS swarmjet_events ON agents;
CREATE TRIGGER swarmjet_events AFTER INSERT OR UPDATE ON agents FOR EACH ROW EXECUTE FUNCTION swarmjet_notify_event('agent');
DROP TRIGGER IF EXISTS swarmjet_events ON attestations;
CREATE TRIGGER swarmjet_events AFTER INSERT OR UPDATE ON attestations FOR EACH ROW EXECUTE FUNCTION swarmjet_notify_event('pr');
DROP TRIGGER IF EXISTS swarmjet_events ON methodology_events;
CREATE TRIGGER swarmjet_events AFTER INSERT OR UPDATE ON methodology_events FOR EACH ROW EXECUTE FUNCTION swarmjet_notify_event('methodology');
DROP TRIGGER IF EXISTS swarmjet_events ON spawn_queue;
CREATE TRIGGER swarmjet_events AFTER INSERT OR UPDATE ON spawn_queue FOR EACH ROW EXECUTE FUNCTION swarmjet_notify_event('queue');
DROP TRIGGER IF EXISTS swarmjet_events ON hives;
CREATE TRIGGER swarmjet_events AFTER INSERT OR UPDATE ON hives FOR EACH ROW EXECUTE FUNCTION swarmjet_notify_event('hive');
