# Cell runtime (`internal/cell/docker`)

Implements `cell.Runtime` and `cell.Publisher` from `internal/cell/cell.go` on
top of the local Docker daemon. Cells run oh-my-pi (`omp acp`) and speak ACP to
it. Model credentials never enter the Cell; the CLI talks to the platform model
proxy with its own Agent token.

## Layout and naming

| Item | Value |
|---|---|
| Container | `swarmjet-cell-<agent-id>` |
| Container labels | `swarmjet.cell=true`, `swarmjet.agent=<agent-id>`, `swarmjet.hive=<hive-id>` |
| Workspace (host) | `<DATA_DIR>/cells/<agent-id>/workspace` |
| Workspace (container) | `/workspace` (bind mount, always the CLI cwd) |
| CLI data volume | `swarmjet-cell-<agent-id>-data` mounted at `/cell` |
| CLI `HOME` | `/cell/home` |
| omp config root | `/cell/home/.omp` (outside the workspace) |
| omp agent dir | `/cell/home/.omp/methodology` (`PI_CODING_AGENT_DIR`) |
| CLI XDG dirs | `/cell/data`, `/cell/config`, `/cell/state` |

The workspace is on the host so the platform can inspect it and the Publisher
can read the bundle the Cell writes. After the first clone, the host never runs
git against the workspace: branch selection, staging, the WIP commit and
`git bundle create` all run through `docker exec` inside the Cell. The named
volume holds omp's agent database and session files; it survives `Stop`
(container stopped) and `Remove` (container deleted, volume kept). Only `Destroy`
deletes the workspace, volume and platform mirror.

## Configuration delivery

`Spec.ConfigFiles` is written below `/cell/home/.omp` before every `Start`:

- `methodology/config.yml` and `methodology/platform.yml`: the platform settings
  overlay. `platform.yml` is passed with `--config`, so it outranks a repository's
  project `.omp/config.yml`.
- `methodology/project.yml`: a second overlay that disables the exact capability
  ids of tools and hooks under the repository's `.omp/tools` and `.omp/hooks`.
  It is passed after `platform.yml` so its `disabledExtensions` array wins.
- `methodology/models.yml`: a custom provider using omp's `pi-native` transport,
  pointing at `Spec.LLMBaseURL` and reading the Agent token from
  `SWARMJET_LLM_TOKEN`.
- `methodology/<relative path>`: methodology files. Because the agent dir is
  `methodology/`, omp discovers `rules/` and `skills/` there natively.
- `methodology/ROLE.md`: the role prompt, appended with
  `--append-system-prompt`.

The runtime writes a `.swarmjet-managed` manifest in the config root. On the
next start it removes only those paths before extracting the new set, so omp's
database and sessions are preserved. Files are installed mode `0444`; the CLI
can read but not write them through ordinary file operations.

The settings overlay sets `tools.approvalMode: yolo` (the container is the
isolation boundary), applies the role's `denyTools`, disables extensions, project
MCP config, browser, desktop control and GitHub (`pr://`/`issue://`) access, and
disables foreign discovery providers. `--no-extensions` disables extension
modules and hook discovery.

## Lifecycle

| Method | Container | Volume | Workspace | Mirror |
|---|---|---|---|---|
| `Start` | create/start, run `omp acp` | keep | clone if empty; branch or revision checkout in Cell | keep |
| `Stop` | stop | keep | keep | keep |
| `Remove` | delete | keep | keep | keep |
| `Destroy` | delete | delete | delete | delete |

`Start` resets a stale in-process session, but a new process can still reconnect
by passing the persisted `Spec.SessionRef`. omp advertises `loadSession: true`;
a failed `session/load` falls back to a fresh session and is logged.

When `Spec.Revision` is set, the Cell creates the local branch `Spec.Branch` at
that commit and detaches HEAD. A missing commit fails with a clear error. The
Publisher refuses any `Spec.ReadOnly` Cell before touching git or GitHub.

## Credential flow and trust boundaries

- **Model credentials never enter the Cell.** The Cell receives only its own
  Agent token (`Spec.LLMToken`/`Spec.MCPToken`). `models.yml` contains the proxy
  URL and the env-var name, never a provider key.
- **MCP** is attached per session as an HTTP MCP server. `Spec.MCPToken` goes
  into the `Authorization` header of `session/new` / `session/load`, never into a
  file.
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

# Real Docker + omp. A local fake pi-native gateway is used unless
# SWARMJET_CELL_E2E_LLM_URL points at a real proxy. Skipped by default.
SWARMJET_CELL_E2E=1 \
SWARMJET_CELL_E2E_IMAGE=swarmjet-agent:local \
go test ./internal/cell/docker/ -run 'TestCell.*E2E' -v -timeout 30m
```

Optional E2E variables: `SWARMJET_CELL_E2E_DOCKER`,
`SWARMJET_CELL_E2E_IMAGE`, `SWARMJET_CELL_E2E_MODEL` (default
`deepseek/deepseek-chat`), `SWARMJET_CELL_E2E_NETWORK` (default `host`) and
`SWARMJET_CELL_E2E_LLM_URL`.

## Known limitations

- Only local Docker on the platform host; multi-Node scheduling is later work.
- Crash consistency of omp's session database is not covered by the E2E test
  (the test uses a clean `Stop` before restarting).
- `session/load` depends on the same cwd (`/workspace`) and the same data volume;
  a future Node move must keep both.
- The data volume is handed to the container user with a one-shot root container
  (`chown`); this requires the configured image to contain `/bin/sh`.
- The repository's `.omp/tools` and `.omp/hooks` are disabled by scanning their
  exact capability ids into `project.yml`. A tool loaded from an installed
  plugin or an explicit extension path is not discoverable this way, but
  `--no-extensions` and the disabled provider list keep those sources off.
