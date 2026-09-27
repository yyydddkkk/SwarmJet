CREATE TABLE IF NOT EXISTS schema_version(version integer PRIMARY KEY);
INSERT INTO schema_version VALUES (1) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS projects(id text PRIMARY KEY, version integer NOT NULL, body jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS tasks(id text PRIMARY KEY, project_id text NOT NULL REFERENCES projects(id), source text NOT NULL, stage text NOT NULL, terminal boolean NOT NULL DEFAULT false, body jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX IF NOT EXISTS one_active_issue ON tasks(source) WHERE NOT terminal;
CREATE TABLE IF NOT EXISTS commands(id text PRIMARY KEY, task_id text NOT NULL REFERENCES tasks(id), body jsonb NOT NULL, delivered boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS pending_commands ON commands(created_at) WHERE NOT delivered;
CREATE TABLE IF NOT EXISTS webhook_inbox (id bigserial PRIMARY KEY, provider text NOT NULL, delivery_id text NOT NULL, event_type text NOT NULL DEFAULT '', payload bytea NOT NULL, received_at timestamptz NOT NULL DEFAULT now(), attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL DEFAULT now(), lease_until timestamptz, processed_at timestamptz, UNIQUE(provider,delivery_id));
CREATE INDEX IF NOT EXISTS pending_webhook_inbox ON webhook_inbox(next_attempt_at,id) WHERE processed_at IS NULL;
CREATE INDEX IF NOT EXISTS processed_webhook_inbox ON webhook_inbox(processed_at) WHERE processed_at IS NOT NULL;
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

-- Kubernetes admission and fencing are authoritative here, not in Pod status
-- or the JSON task projection. Rows are retained after completion for audit.
CREATE TABLE IF NOT EXISTS kubernetes_admissions (
 task_id text PRIMARY KEY REFERENCES tasks(id), pool text NOT NULL,
 resources jsonb NOT NULL, state text NOT NULL CHECK (state IN ('waiting','admitted','cancelled')),
 created_at timestamptz NOT NULL DEFAULT now(), admitted_at timestamptz
);
CREATE INDEX IF NOT EXISTS kubernetes_admissions_queue ON kubernetes_admissions(pool,created_at,task_id) WHERE state='waiting';
CREATE TABLE IF NOT EXISTS kubernetes_executions (
 execution_id text PRIMARY KEY, task_id text NOT NULL REFERENCES tasks(id), role text NOT NULL,
 authority_version bigint NOT NULL DEFAULT 0,
 state text NOT NULL CHECK (state IN ('pending','active','revoked','stopped','completed','cancelled')),
 attempt_id text NOT NULL DEFAULT '', checkpoint_valid boolean NOT NULL DEFAULT false,
 job_creation text NOT NULL DEFAULT 'none' CHECK (job_creation IN ('none','creating','created')),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE kubernetes_executions ADD COLUMN IF NOT EXISTS job_creation text NOT NULL DEFAULT 'none';
CREATE INDEX IF NOT EXISTS kubernetes_executions_task ON kubernetes_executions(task_id,created_at);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_kubernetes_execution ON kubernetes_executions(task_id) WHERE state IN ('pending','active','revoked','stopped');

-- One immutable intent precedes each trusted external mutation. A lost response
-- leaves the intent pending so recovery can read remote state before retrying.
CREATE TABLE IF NOT EXISTS kubernetes_write_intents (
 id text PRIMARY KEY, task_id text NOT NULL REFERENCES tasks(id),
 execution_id text NOT NULL REFERENCES kubernetes_executions(execution_id),
 authority_version bigint NOT NULL, kind text NOT NULL CHECK (kind IN ('push','mr','merge')),
 repository_id text NOT NULL, project_id bigint NOT NULL,
 source_branch text NOT NULL, target_branch text NOT NULL, head_sha text NOT NULL,
 state text NOT NULL CHECK (state IN ('pending','confirmed')) DEFAULT 'pending',
 remote_id bigint NOT NULL DEFAULT 0, created_at timestamptz NOT NULL DEFAULT now(),
 confirmed_at timestamptz
);
CREATE INDEX IF NOT EXISTS kubernetes_write_intents_pending ON kubernetes_write_intents(task_id,created_at) WHERE state='pending';

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
