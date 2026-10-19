# CLI login service

`NewInProcess(Config)` returns the handler mounted by `serve` at `/api/runtime/*`. The console session authenticates the caller; the handler is only enabled when Docker is reachable (the Cells compose overlay). `New(Config)` keeps the older worker-private secret check for standalone use and tests. Neither form logs terminal messages or keeps transcripts.

Routes:

- `GET /status`: image availability, detected CLI installation when the setup container is running, configured provider names, configuration presence, and Codex auth presence. It does not create/start an absent or stopped container. `toolsChecked=false` means the installed-tool flags have not yet been checked; it does not mean the tools are missing. `agents` reports installation/config-file presence; config presence is not proof of valid authentication.
- `GET /models`: starts the setup container if necessary, executes only `opencode models` with a 30-second timeout and a 2 MiB output cap, and returns catalog IDs belonging to configured provider names. This does not check credentials, credits, quotas, or model access and does not invoke a model.
- `GET /terminal`: WebSocket terminal, one session per profile across service processes, maximum 60 minutes. Client messages are JSON `{"type":"input","data":"..."}` or `{"type":"resize","cols":120,"rows":40}`. Server terminal output is binary; text frames contain `{"type":"error","message":"..."}` or `{"type":"exit","code":0}`. Closing the session stops only its ownership-checked setup container so shell children cannot remain running.

`OpenCodeAuth(dataDir)` returns the login state written by the terminal, and `LoggedIn(dataDir)` reports only whether it exists and is valid. `serve` reads it on every Cell start and passes it to the Cell as `OPENCODE_AUTH_CONTENT`; when absent it falls back to the deployment environment variable. The credential is never logged, persisted by this package, or returned by an API.

`Config.Image` is the deployment image. The container is `sj-config-<SHA256-prefix-of-absolute-dataDir>`, with `swarmjet.runtime-profile=default` and `swarmjet.runtime-root=<same hash>` ownership labels. An unowned same-name container is never adopted, stopped, or removed. Owned containers may be recreated when the configured image changes, retaining profile files.

`GetProfileDirs(dataDir)` returns these host paths:

| Field | Host path |
|---|---|
| Home | `<dataDir>/runtime-profiles/default/home` |
| OpenCodeData | `<Home>/.local/share/opencode` |
| OpenCodeConfig | `<Home>/.config/opencode` |
| CodexHome | `<Home>/.codex` |

Only `Home` is mounted, at `/runtime-home`. The container runs with the serve process's UID:GID so credential/config files remain readable and do not become inaccessible root-owned files when serve runs as a normal user. The container receives `HOME=/runtime-home`, `XDG_DATA_HOME=/runtime-home/.local/share`, `XDG_CONFIG_HOME=/runtime-home/.config`, and `CODEX_HOME=/runtime-home/.codex`. It receives no business workspace, task state, data-root mount, or Docker socket. Shell history is disabled in both the container and terminal exec environment using `HISTFILE=/dev/null`, `HISTSIZE=0`, and `HISTFILESIZE=0`; Bash runs with `--noprofile --norc`.

Read-only metadata supports OpenCode `.local/share/opencode/auth.json`, `.config/opencode/opencode.json{,c}`, legacy `.config/opencode/config.json{,c}`, and home-level `opencode.json{,c}`. Only auth object keys/provider configuration keys are returned, never their values. Codex `.codex/auth.json` is reported only as a configured boolean. File reads are bounded and use `openat(O_NOFOLLOW)` on every path component; symlinks and non-regular files are rejected. A malformed/unsafe file yields a generic warning.

The task runtime copies only `.local/share/opencode/auth.json` into its separate Cell home (as the `OPENCODE_AUTH_CONTENT` value); it must never mount this shared profile into task containers. OpenCode sessions, prompt history, unrelated home files, and `.codex` state are not copied into Cell execution merely because they share the setup home. The file is read through the same `O_NOFOLLOW` path check and is never logged.

`Config.Env` accepts only known provider API-key variables (OpenAI, Anthropic, Google/Gemini, DeepSeek, OpenRouter, Groq, xAI, Mistral, Azure OpenAI). `OPENCODE_CONFIG_CONTENT` is also accepted for deployment-provided OpenCode JSON/JSONC configuration, bounded to 64 KiB; only provider keys are exposed in metadata. Other unknown entries are discarded, including GitLab, database, and administrator credentials. Docker receives allowed values through its process environment, not command-line argument values. Docker itself is a trusted administrative boundary and necessarily stores the configured container environment.

Use device/browser authorization supported by the installed CLI; for example, Codex device login can be performed from this terminal. The service does not expose arbitrary localhost callback ports. `DockerCommand` and `TerminalCommand` are injectable for isolated lifecycle/PTY tests; production uses the fixed `docker` executable and fixed shell command.

Opt-in real Docker validation uses only a new temporary profile: `SWARMJET_RUNTIME_IMAGE=swarmjet-agent:config-ui go test ./internal/runtimeadmin -run TestActualDockerTerminal -v`. It verifies actual OpenCode/Codex launch, PTY input/output and resize, disabled history, disconnect shutdown, and profile-file persistence across reconnects, then removes only its owned setup container and temporary directory. It does not log in, query paid models, or read real authentication.
