# OpenCode runtime

`New(dataDir)` exposes `Start(ctx, AgentRequest)`, `Inspect(ctx, executionID)`,
`Resume(ctx, AgentRequest)`, and `Cancel(ctx, executionID)`.

The pipeline depends on `execution.AgentBackend` rather than `*agent.Runtime`.
This runtime is the local Docker implementation. Its lifecycle contract includes
task-wide enumeration/cancellation so recovery also accounts for old rework runs.
`Resume` remains a convenience method; the interface resumes an exact session by
passing `SessionID` to `Start`.

When `AgentRequest.Env` includes `GITLAB_URI`, the runtime prepares private glab
configuration inside the execution directory and supplies the image's Git
credential-helper path. The pipeline supplies the URL and phase-specific token,
not a path to a configuration file created on the orchestration host. The request
environment is copied before adding runtime-specific values.

Requests require adapter `opencode`, an explicit container image containing the
`opencode` executable, a `provider/model` model, an absolute workspace beneath
`dataDir`, stable task/execution IDs, and role `dev`, `review`, or `merge`.
`Resume` requires the exact prior `SessionID`; it never uses `--continue`.
The Docker daemon must see `dataDir` at the same absolute path as the worker.
The runtime executes only Docker; `Executor` and `DockerBinary` permit testing
and installation-specific Docker client paths.

Start creates the named container `sj-agent-{executionID}` and returns without
waiting for the model. Inspect reconciles its state. Retrying Start with the same
request adopts an existing container or reconstructs an interrupted initial
create. A worker recovering a `starting` execution whose container does not yet
exist must retry Start with the original request, including resolved credential
environment, instead of only polling Inspect. Retain containers until the task's
retention policy removes them; their state and configuration support recovery.

Administrator setup lives in `{dataDir}/runtime-profiles/default/home` and is
accessible through the authenticated runtime environment page. It is never
mounted into a task. On first role initialization, the runtime copies OpenCode
`auth.json` and `opencode.json/jsonc` into private task-role directories.
`XDG_DATA_HOME={dataDir}/opencode/{taskID}/{dev|review}` holds private history/auth;
`{scope}-config` is a writable task/role configuration copy so OpenCode can create
startup files such as `.gitignore`. The shared profile is never mounted into the
agent. Merge reuses review.
Agent containers run with the supervisor's UID/GID and use the private role data
directory as HOME, so native workers can read and reuse their generated files.
Existing role credentials/history are retained, including refreshed tokens;
new roles receive the current configured profile. An empty profile may be seeded
after the administrator logs in. Legacy `{dataDir}/opencode-config` remains a
fallback for configuration only.

Environment-based provider configuration still works via `AgentRequest.Env`.
Supervisor metadata stores paths and variable names, never credential values.
Initial credential snapshots are kept outside container mounts so old and
refreshed tokens can both be redacted from results and logs. Docker access remains
privileged because the daemon holds execution environment variables.

Each execution gets `{dataDir}/runs/{taskID}/executions/{executionID}/`:

- `prompt.txt`: private task instructions, attached with `--file`.
- `output.jsonl`: bounded, redacted Docker stdout event log.
- `result.json`: agent-written completion artifact; required even for blocked work.

The artifact schema is `domain.AgentResult`. A nonempty summary and role-valid
outcome are required. Blocked results also require `needsHelp`. Dev outcomes are
`changed`, `no_change`, `blocked`; review outcomes are `approved`,
`changes_requested`, `blocked`; merge outcomes are `merged`, `blocked`.
Only exit zero plus a valid artifact produces execution status `succeeded`.
A succeeded execution can have a blocked business outcome; callers must inspect
`Result.Outcome`. Other terminal statuses are `failed`, `cancelled`, `timed_out`.
Persisted execution metadata contains neither prompt text nor environment values;
result fields and logs are redacted using the recorded environment variable names
and the recovered container environment. The agent-owned prompt/result artifacts
and OpenCode session store remain private execution data and require appropriate
retention and filesystem access controls.

CLI flags verified against the official [OpenCode CLI documentation](https://dev.opencode.ai/docs/cli/):
`run --format json --auto --dir ... -m ... --file ...`, plus `--session ...` for
resumption. No shell interpolates prompts, paths, models, or credentials.
