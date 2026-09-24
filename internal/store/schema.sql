CREATE TABLE IF NOT EXISTS schema_version(version integer PRIMARY KEY);
INSERT INTO schema_version VALUES (1) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS projects(id text PRIMARY KEY, version integer NOT NULL, body jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS tasks(id text PRIMARY KEY, project_id text NOT NULL REFERENCES projects(id), source text NOT NULL, stage text NOT NULL, terminal boolean NOT NULL DEFAULT false, body jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX IF NOT EXISTS one_active_issue ON tasks(source) WHERE NOT terminal;
CREATE TABLE IF NOT EXISTS commands(id text PRIMARY KEY, task_id text NOT NULL REFERENCES tasks(id), body jsonb NOT NULL, delivered boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS pending_commands ON commands(created_at) WHERE NOT delivered;
CREATE TABLE IF NOT EXISTS events(id bigserial PRIMARY KEY, task_id text NOT NULL, kind text NOT NULL, message text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS task_events ON events(task_id,id);
CREATE TABLE IF NOT EXISTS sessions(token_hash text PRIMARY KEY, expires_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS leases(name text PRIMARY KEY, owner text NOT NULL, expires_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS connections(id text PRIMARY KEY, version integer NOT NULL, body jsonb NOT NULL, secret text NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS initializations(id text PRIMARY KEY, project_id text NOT NULL, status text NOT NULL, body jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS active_initializations ON initializations(status) WHERE status NOT IN ('ready','cancelled');
CREATE TABLE IF NOT EXISTS repository_changes(id text PRIMARY KEY, project_id text NOT NULL REFERENCES projects(id), status text NOT NULL, body jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX IF NOT EXISTS one_active_repository_change ON repository_changes(project_id) WHERE status IN ('pending','waiting');
CREATE TABLE IF NOT EXISTS nodes(id text PRIMARY KEY, address text NOT NULL, capacity integer NOT NULL, version text NOT NULL DEFAULT '', last_seen timestamptz NOT NULL);
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS node_id text;
CREATE INDEX IF NOT EXISTS tasks_by_node ON tasks(node_id) WHERE NOT terminal;

CREATE TABLE IF NOT EXISTS platform_settings(name text PRIMARY KEY, body jsonb NOT NULL);

-- Managed execution environment configuration. Credentials are encrypted separately.
CREATE TABLE IF NOT EXISTS network_profiles(id text NOT NULL, version integer NOT NULL, body jsonb NOT NULL, secret text NOT NULL DEFAULT '', PRIMARY KEY(id,version));
CREATE TABLE IF NOT EXISTS node_configs(id text PRIMARY KEY REFERENCES nodes(id), version integer NOT NULL, body jsonb NOT NULL);
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS readiness jsonb NOT NULL DEFAULT '{}';
CREATE TABLE IF NOT EXISTS node_credentials(id text PRIMARY KEY, secret text NOT NULL, revoked boolean NOT NULL DEFAULT false);

-- Explicit issue comments request the next task round. A note is claimed in
-- the same transaction that creates its task, so webhook/poll retries are safe.
CREATE TABLE IF NOT EXISTS issue_run_commands(source text NOT NULL, note_id bigint NOT NULL, body text NOT NULL, created_at timestamptz NOT NULL, claimed_task_id text REFERENCES tasks(id), PRIMARY KEY(source,note_id));
CREATE INDEX IF NOT EXISTS pending_issue_run_commands ON issue_run_commands(source,created_at,note_id) WHERE claimed_task_id IS NULL;
CREATE TABLE IF NOT EXISTS feature_epochs(name text PRIMARY KEY, enabled_at timestamptz NOT NULL);
INSERT INTO feature_epochs(name,enabled_at) VALUES('issue_round_comments',now()) ON CONFLICT DO NOTHING;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS gitlab_synced_at timestamptz;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS gitlab_sync_retry_at timestamptz;
CREATE INDEX IF NOT EXISTS tasks_pending_gitlab_sync ON tasks(updated_at) WHERE gitlab_synced_at IS NULL OR updated_at > gitlab_synced_at;
CREATE INDEX IF NOT EXISTS tasks_page ON tasks(updated_at DESC,id DESC);
CREATE INDEX IF NOT EXISTS tasks_by_project_page ON tasks(project_id,updated_at DESC,id DESC);

-- Existing nodes require explicit environment configuration before new managed work.
INSERT INTO node_configs(id,version,body) SELECT id,1,jsonb_build_object('version',1,'name',id,'enabled',false,'archived',false,'capacity',capacity,'network',jsonb_build_object('mode','default'),'image','') FROM nodes ON CONFLICT(id) DO NOTHING;
