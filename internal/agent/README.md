# Agent CLI runtime


New Issue tasks select one of `opencode`, `codex`, `dst`, `agy`, or `claude` through the shared catalog (`internal/agentcli`). `adapter.go` owns argv, environment, exact-session extraction and terminal-event failures. Docker lifecycle and `AgentResult` validation stay common. Agents are not launched to probe model availability.

The following OpenCode-specific paths describe **legacy tasks and static initialization**. New tasks have `AgentConfig.UseCLIDefaults=true`, an empty model override, and a task snapshot under `agent-profiles/{taskID}/{adapter}/snapshot`. `dev` and `review` homes are seeded from that immutable snapshot, not independently from the live master profile. Merge uses `review` when independent review is enabled; otherwise it reuses the development home and exact session. Snapshots are taken on first execution, including empty profiles, and retained with the task. Only the catalog's explicit configuration/credential file allowlist is copied, with bounded no-symlink reads and atomic publication. New homes use normal HOME/XDG locations plus CODEX_HOME or DSH_HOME. Both original and refreshed credentials are redacted. New CLI adapters require independent command and event fixtures; optional real-provider tests are separate.

`dst` invokes `dsh --profile headless --json`, not the interactive TUI. On the first task execution, the TUI's saved `.dsh-tui/model.json` provider/model choice is converted into a private `--patch` overlay for headless and frozen with the task profile; allowlisted `.dsh` settings and credentials are copied into the same private home. Antigravity requires portable API-key/ADC authentication; shared OS keyrings are not supported. Static initialization remains OpenCode-only because its policy forbids shell execution/delegation.

DSH deliberately scrubs credential-shaped environment variables from Agent-run shell commands. When a DST execution receives a phase-scoped GitLab token, its launcher writes a private `glab` configuration to container tmpfs, then clears the token from the DSH process environment. A credential-free askpass script in the execution directory reads that tmpfs configuration for Git. Neither task logs nor the persistent `glab` seed config contain the token; the tmpfs disappears with the Runner. The Docker container configuration retains the original environment until task cleanup, as with other adapters.

CLI contracts: [Codex](https://developers.openai.com/codex/noninteractive/), [Claude Code](https://code.claude.com/docs/en/headless), [DSH headless](https://github.com/deepseek-ai/deepseek-harness/tree/main/packages/bundle/headless), [Antigravity](https://antigravity.google/docs/cli/headless/).

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

Legacy requests require adapter `opencode`, an explicit container image containing the
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
agent. Merge reuses review when enabled, or development when review is off.
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
A zero-exit execution with a missing or invalid artifact is marked with
`failureCode=invalid_result`. The pipeline makes one fresh, bounded result-only
execution with the task source tree mounted read-only. It must still supply a
valid artifact; a second failure blocks the task. Retrying an older blocked task
recognizes the previous `invalid result.json` reason and starts at this handoff
without repeating its completed development or publication run.
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
