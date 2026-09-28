CREATE TABLE IF NOT EXISTS schema_version(version integer PRIMARY KEY);
INSERT INTO schema_version VALUES (1) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS projects(id text PRIMARY KEY, version integer NOT NULL, body jsonb NOT NULL);
CREATE TABLE IF NOT EXISTS tasks(id text PRIMARY KEY, project_id text NOT NULL REFERENCES projects(id), source text NOT NULL, stage text NOT NULL, terminal boolean NOT NULL DEFAULT false, body jsonb NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX IF NOT EXISTS one_active_issue ON tasks(source) WHERE NOT terminal;
CREATE TABLE IF NOT EXISTS commands(id text PRIMARY KEY, task_id text NOT NULL REFERENCES tasks(id), body jsonb NOT NULL, delivered boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS pending_commands ON commands(created_at) WHERE NOT delivered;
ALTER TABLE commands ADD COLUMN IF NOT EXISTS published_at timestamptz;
CREATE INDEX IF NOT EXISTS unpublished_commands ON commands(published_at,created_at) WHERE NOT delivered;
CREATE TABLE IF NOT EXISTS webhook_inbox (id bigserial PRIMARY KEY, provider text NOT NULL, delivery_id text NOT NULL, event_type text NOT NULL DEFAULT '', payload bytea NOT NULL, received_at timestamptz NOT NULL DEFAULT now(), attempts integer NOT NULL DEFAULT 0, next_attempt_at timestamptz NOT NULL DEFAULT now(), lease_until timestamptz, processed_at timestamptz, UNIQUE(provider,delivery_id));
CREATE INDEX IF NOT EXISTS pending_webhook_inbox ON webhook_inbox(next_attempt_at,id) WHERE processed_at IS NULL;
CREATE INDEX IF NOT EXISTS processed_webhook_inbox ON webhook_inbox(processed_at) WHERE processed_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS webhook_receipts(provider text NOT NULL, delivery_id text NOT NULL, content_hash bytea NOT NULL, processed_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(provider,delivery_id));
CREATE TABLE IF NOT EXISTS events(id bigserial PRIMARY KEY, task_id text NOT NULL, kind text NOT NULL, message text NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
CREATE INDEX IF NOT EXISTS task_events ON events(task_id,id);
CREATE TABLE IF NOT EXISTS sessions(token_hash text PRIMARY KEY, expires_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS users (
 id text PRIMARY KEY, email text NOT NULL UNIQUE, display_name text NOT NULL,
 password_hash text NOT NULL, status text NOT NULL CHECK (status IN ('pending_email','pending_approval','active','disabled','rejected')),
 platform_admin boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS user_id text REFERENCES users(id);
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS principal_type text NOT NULL DEFAULT 'legacy';
-- Revoke pre-authorization cookies once. New breakglass cookies use a distinct type.
DELETE FROM sessions WHERE principal_type='legacy';
CREATE INDEX IF NOT EXISTS sessions_by_user ON sessions(user_id);
CREATE TABLE IF NOT EXISTS email_tokens (
 token_hash text PRIMARY KEY, user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 kind text NOT NULL CHECK (kind IN ('verify','reset')), expires_at timestamptz NOT NULL,
 consumed_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS email_tokens_by_user ON email_tokens(user_id,kind) WHERE consumed_at IS NULL;
CREATE TABLE IF NOT EXISTS teams (id text PRIMARY KEY, name text NOT NULL UNIQUE, created_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS team_members (team_id text NOT NULL REFERENCES teams(id) ON DELETE CASCADE, user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE, PRIMARY KEY(team_id,user_id));
CREATE TABLE IF NOT EXISTS project_grants (project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE, user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE, role text NOT NULL CHECK (role IN ('owner','operator','viewer')), PRIMARY KEY(project_id,user_id));
CREATE TABLE IF NOT EXISTS team_project_grants (project_id text NOT NULL REFERENCES projects(id) ON DELETE CASCADE, team_id text NOT NULL REFERENCES teams(id) ON DELETE CASCADE, role text NOT NULL CHECK (role IN ('owner','operator','viewer')), PRIMARY KEY(project_id,team_id));
CREATE TABLE IF NOT EXISTS external_identities (
 user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE, provider text NOT NULL,
 instance text NOT NULL, external_id text NOT NULL, username text NOT NULL,
 secret text NOT NULL DEFAULT '', updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,provider,instance), UNIQUE(provider,instance,external_id)
);
CREATE TABLE IF NOT EXISTS audit_events (
 id bigserial PRIMARY KEY, actor_user_id text REFERENCES users(id) ON DELETE SET NULL,
 actor_type text NOT NULL, action text NOT NULL, target_type text NOT NULL,
 target_id text NOT NULL, details jsonb NOT NULL DEFAULT '{}'::jsonb,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audit_events_recent ON audit_events(created_at DESC,id DESC);
ALTER TABLE audit_events ADD COLUMN IF NOT EXISTS dedupe_key text;
CREATE UNIQUE INDEX IF NOT EXISTS audit_events_dedupe ON audit_events(dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE TABLE IF NOT EXISTS denied_issue_run_commands (
 source text NOT NULL, note_id bigint NOT NULL, reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(source,note_id)
);
CREATE TABLE IF NOT EXISTS denied_issue_label_events (
 source text NOT NULL, event_id bigint NOT NULL, reason text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(source,event_id)
);
CREATE TABLE IF NOT EXISTS leases(name text PRIMARY KEY, owner text NOT NULL, expires_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS connections(id text PRIMARY KEY, version integer NOT NULL, body jsonb NOT NULL, secret text NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS gitlab_identities(instance text PRIMARY KEY, username text NOT NULL, secret text NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());
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

-- The index contains only Task ownership. GitLab remains the source of MR content.
CREATE TABLE IF NOT EXISTS task_mrs(instance text NOT NULL, gitlab_project_id bigint NOT NULL, mr_iid bigint NOT NULL, task_id text NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, repository_id text NOT NULL, PRIMARY KEY(instance,gitlab_project_id,mr_iid));
CREATE INDEX IF NOT EXISTS task_mrs_by_task ON task_mrs(task_id);
INSERT INTO task_mrs(instance,gitlab_project_id,mr_iid,task_id,repository_id)
SELECT regexp_replace(trim(trailing '/' from t.body->'project'->>'gitlabUrl'), '/api/v4$', ''), (r.value->>'projectId')::bigint, (r.value->>'mrIid')::bigint, t.id, r.value->>'repositoryId'
FROM tasks t CROSS JOIN LATERAL jsonb_array_elements(coalesce(t.body->'workspace'->'revisions','[]'::jsonb)) AS r(value)
WHERE coalesce(t.body->'project'->>'provider','gitlab') <> 'github'
AND coalesce(r.value->>'mrIid','0') ~ '^[0-9]+$' AND (r.value->>'mrIid')::bigint > 0
AND coalesce(r.value->>'projectId','0') ~ '^[0-9]+$' AND (r.value->>'projectId')::bigint > 0
ON CONFLICT(instance,gitlab_project_id,mr_iid) DO NOTHING;
CREATE TABLE IF NOT EXISTS task_manual_acceptance(task_id text PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE, digest text NOT NULL, checks jsonb NOT NULL, note text NOT NULL, updated_at timestamptz NOT NULL DEFAULT now());

-- Existing nodes require explicit environment configuration before new managed work.
INSERT INTO node_configs(id,version,body) SELECT id,1,jsonb_build_object('version',1,'name',id,'enabled',false,'archived',false,'capacity',capacity,'network',jsonb_build_object('mode','default'),'image','') FROM nodes ON CONFLICT(id) DO NOTHING;
