# Runtime setup service

`New(Config)` returns a worker-private HTTP handler. Every route requires the constant-time checked `X-SwarmJet-Runtime-Secret`. Mount it on the private worker network; the browser-facing API must independently authenticate the user and validate WebSocket Origin before adding this shared secret. The worker does not log terminal messages or keep transcripts.

Routes:

- `GET /status`: image availability, detected CLI installation when the setup container is running, configured provider names, configuration presence, and Codex auth presence. It does not create/start an absent or stopped container. `toolsChecked=false` means the installed-tool flags have not yet been checked; it does not mean the tools are missing. `agents` reports installation/config-file presence for all five supported CLIs; config presence is not proof of valid authentication. `codex.executionSupported` is true. Platform enablement is separately stored in PostgreSQL through the console API `/api/runtime/agents`, not in worker-local profile metadata.
- `GET /models`: starts the setup container if necessary, executes only `opencode models` with a 30-second timeout and a 2 MiB output cap, and returns catalog IDs belonging to configured provider names. This does not check credentials, credits, quotas, or model access and does not invoke a model.
- `GET /terminal`: WebSocket terminal, one session per profile across service processes, maximum 60 minutes. Client messages are JSON `{"type":"input","data":"..."}` or `{"type":"resize","cols":120,"rows":40}`. Server terminal output is binary; text frames contain `{"type":"error","message":"..."}` or `{"type":"exit","code":0}`. Closing the session stops only its ownership-checked setup container so shell children cannot remain running.

`Config.Image` is the deployment fallback. The authenticated image-management routes may activate a successfully built or inspected local image by immutable ID; arbitrary mounts remain unavailable. The container is `sj-config-<SHA256-prefix-of-absolute-dataDir>`, with `swarmjet.runtime-profile=default` and `swarmjet.runtime-root=<same hash>` ownership labels. An unowned same-name container is never adopted, stopped, or removed. Owned containers may be recreated when the configured image changes, retaining profile files.

`GetProfileDirs(dataDir)` returns these host paths:

| Field | Host path |
|---|---|
| Home | `<dataDir>/runtime-profiles/default/home` |
| OpenCodeData | `<Home>/.local/share/opencode` |
| OpenCodeConfig | `<Home>/.config/opencode` |
| CodexHome | `<Home>/.codex` |

Only `Home` is mounted, at `/runtime-home`. The container runs with the worker’s UID:GID so credential/config files remain readable by the worker and do not become inaccessible root-owned files when the worker runs as a normal user. The container receives `HOME=/runtime-home`, `XDG_DATA_HOME=/runtime-home/.local/share`, `XDG_CONFIG_HOME=/runtime-home/.config`, and `CODEX_HOME=/runtime-home/.codex`. It receives no business workspace, task state, data-root mount, or Docker socket. Shell history is disabled in both the container and terminal exec environment using `HISTFILE=/dev/null`, `HISTSIZE=0`, and `HISTFILESIZE=0`; Bash runs with `--noprofile --norc`.

Read-only metadata supports OpenCode `.local/share/opencode/auth.json`, `.config/opencode/opencode.json{,c}`, legacy `.config/opencode/config.json{,c}`, and home-level `opencode.json{,c}`. Only auth object keys/provider configuration keys are returned, never their values. Codex `.codex/auth.json` is reported only as a configured boolean. File reads are bounded and use `openat(O_NOFOLLOW)` on every path component; symlinks and non-regular files are rejected. A malformed/unsafe file yields a generic warning.

The task runtime may **copy a reviewed allowlist of these credential/config files once into its separate task-role home**; it must never mount this shared profile into task containers. OpenCode sessions, prompt history, unrelated home files, and `.codex` state should not be copied into OpenCode task execution merely because they share the setup home. Preserve restrictive permissions and reject symlinks when copying. Copying is intentionally outside this administrative service.

`Config.Env` accepts only known provider API-key variables (OpenAI, Anthropic, Google/Gemini, DeepSeek, OpenRouter, Groq, xAI, Mistral, Azure OpenAI). `OPENCODE_CONFIG_CONTENT` is also accepted for deployment-provided OpenCode JSON/JSONC configuration, bounded to 64 KiB; only provider keys are exposed in metadata. Other unknown entries are discarded, including GitLab, database, and administrator credentials. Docker receives allowed values through its process environment, not command-line argument values. Docker itself is a trusted administrative boundary and necessarily stores the configured container environment.

Use device/browser authorization supported by the installed CLI; for example, Codex device login can be performed from this terminal. The service does not expose arbitrary localhost callback ports. `DockerCommand` and `TerminalCommand` are injectable for isolated lifecycle/PTY tests; production uses the fixed `docker` executable and fixed shell command.

Opt-in real Docker validation uses only a new temporary profile: `SWARMJET_RUNTIME_IMAGE=swarmjet-agent:config-ui go test ./internal/runtimeadmin -run TestActualDockerTerminal -v`. It verifies actual OpenCode/Codex launch, PTY input/output and resize, disabled history, disconnect shutdown, and profile-file persistence across reconnects, then removes only its owned setup container and temporary directory. It does not log in, query paid models, or read real authentication.


## Node environments

The configuration terminal uses the deployment `AGENT_IMAGE`. Images are not built here; Nodes pull the image named in their configuration.

Node runtime handlers use the Node's applied image and network revision. Open terminal requests pin their handler; after they close, a changed revision recreates the configuration container. Proxy variables are included, including explicit empty values for direct mode. Node configuration files remain local; task profiles are private snapshots.
