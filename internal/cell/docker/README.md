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
can read the bundle the Cell writes. After the first clone, the host never runs
git against the workspace: branch selection, staging, the WIP commit and
`git bundle create` all run through `docker exec` inside the Cell. The named
volume holds OpenCode's SQLite session database; it survives `Stop` (container
stopped) and `Remove` (container deleted, volume kept). Only `Destroy` deletes
the workspace, volume and platform mirror.

The Publisher keeps a platform-owned bare mirror at
`<DATA_DIR>/cells/<agent-id>/mirror.git`. It is not mounted into the Cell. The
host imports exactly `refs/heads/<Spec.Branch>` from the Cell's bundle into the
mirror, verifies that the current base commit is an ancestor of that branch,
and pushes from the mirror with platform credentials.

## Lifecycle

| Method | Container | Volume | Workspace | Mirror |
|---|---|---|---|---|
| `Start` | create/start, run `opencode acp` | keep | clone if empty; branch checkout in Cell | keep |
| `Stop` | stop | keep | keep | keep |
| `Remove` | delete | keep | keep | keep |
| `Destroy` | delete | delete | delete | delete |

`Start` resets a stale in-process session, but a new process can still reconnect
by passing the persisted `Spec.SessionRef`; a failed `session/load` falls back to
a fresh session and is logged.

## Credential flow and trust boundaries

- **Model credentials** (`Spec.Env`, for example `OPENCODE_AUTH_CONTENT`) are
  passed only as `docker exec -e KEY=VALUE` to the CLI process. They are not
  baked into the image, not in the create arguments, and are redacted from
  errors and log lines. Commands run for git inside the Cell do not receive
  them.
- **MCP** is attached per session as an HTTP MCP server. `Spec.MCPToken` goes
  into the `Authorization` header of `session/new` / `session/load`, never into a
  file or the container environment.
- **First clone credentials** come from a `TokenSource`. git gets a temporary
  askpass helper (no secret in the file) and `SWARMJET_GIT_TOKEN` in its
  environment. The remote URL is always credential-free.
- **Workspace git is Cell-side.** `.git/config` and `.gitattributes` in the
  workspace are Cell-writable and therefore untrusted on the host. After the
  first clone, the host runs no git command with the workspace as repository.
  Malicious `url.*.insteadOf`, URL-scoped credential helpers, `filter.*`
  clean/process drivers, `include.path`, `core.hooksPath` and `gpg.program` can
  only execute inside the container.
- **The bundle is untrusted data.** The host copies it to a platform-owned
  temporary file (`O_NOFOLLOW`, regular file, size limit) and accepts only
  `refs/heads/<Spec.Branch>`; prerequisites and any other ref are rejected.
  Before pushing, the mirror checks that the remote base commit is an ancestor
  of the fetched branch.
- **Publishing** runs on the platform side. The Cell never receives Git
  credentials. The Publisher pushes exactly one refspec from the mirror with the
  platform's token and then calls the GitHub client.
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
