# Cell runtime (`internal/cell/docker`)

Implements `cell.Runtime` and `cell.Publisher` from `internal/cell/cell.go` on
top of the local Docker daemon.

## Layout and naming

| Item | Value |
|---|---|
| Container | `swarmjet-cell-<agent-id>` |
| Container labels | `swarmjet.cell=true`, `swarmjet.agent=<agent-id>`, `swarmjet.hive=<hive-id>` |
| Workspace (host) | `<DATA_DIR>/cells/<agent-id>/workspace` |
| Workspace (container) | `/workspace` (bind mount, always the CLI cwd) |
| CLI data volume | `swarmjet-cell-<agent-id>-data` mounted at `/cell` |
| CLI `HOME` | `/cell/home` |
| CLI XDG dirs | `/cell/data`, `/cell/config`, `/cell/state` |

The workspace is on the host so the platform can inspect it and the Publisher
can run git against it. The named volume holds OpenCode's SQLite session
database; it survives `Stop` (container stopped) and `Remove` (container
deleted, volume kept). Only `Destroy` deletes both.

## Lifecycle

| Method | Container | Volume | Workspace |
|---|---|---|---|
| `Start` | create/start, run `opencode acp` | keep | clone if empty, checkout the Agent branch |
| `Stop` | stop | keep | keep |
| `Remove` | delete | keep | keep |
| `Destroy` | delete | delete | delete |

`Start` resets a stale in-process session, but a new process can still reconnect
by passing the persisted `Spec.SessionRef`; a failed `session/load` falls back to
a fresh session and is logged.

## Credential flow

- **Model credentials** (`Spec.Env`, for example `OPENCODE_AUTH_CONTENT`) are
  passed only as `docker exec -e KEY=VALUE`. They are not baked into the image,
  not in the create arguments, and are redacted from errors and log lines.
- **MCP** is attached per session as an HTTP MCP server. `Spec.MCPToken` goes
  into the `Authorization` header of `session/new` / `session/load`, never into a
  file or the container environment.
- **Git clone credentials** come from a `TokenSource`. git gets a temporary
  askpass helper (no secret in the file) and `SWARMJET_GIT_TOKEN` in its
  environment. The remote URL is always credential-free.
- **Publishing** runs on the platform side. The Cell never receives Git
  credentials; the Publisher pushes exactly `Spec.Branch` with the platform's
  token and then calls the GitHub client.
- **Tool permission** requests are answered with `allow_once`; the container is
  the isolation boundary. Each approval is emitted as a `cell.Update` with
  `Title: "permission: <tool>"`.

## Testing

```console
# Unit tests: fake docker executor, scripted ACP client, local bare repos.
go test -race ./internal/cell/docker/

# Real Docker + OpenCode + free model. Skipped by default.
SWARMJET_CELL_E2E=1 \
SWARMJET_CELL_E2E_IMAGE=swarmjet-agent:local \
go test ./internal/cell/docker/ -run TestCellRuntimeE2E -v -timeout 30m
```

Optional E2E variables: `SWARMJET_CELL_E2E_DOCKER`,
`SWARMJET_CELL_E2E_IMAGE`, `SWARMJET_CELL_E2E_MODEL` (default
`opencode/big-pickle`).

## Known limitations

- Only local Docker on the platform host; multi-Node scheduling is later work.
- Crash consistency of the OpenCode SQLite/WAL database is not covered by the
  E2E test (the test uses a clean `Stop` before restarting).
- `session/load` depends on the same cwd (`/workspace`) and the same data volume;
  a future Node move must keep both.
- The data volume is handed to the container user with a one-shot root container
  (`chown`); this requires the configured image to contain `/bin/sh`.
