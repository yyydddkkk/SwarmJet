# Round 8 PR list performance evidence

The benchmark is `TestPRListColdAndWarmWithDelayedGitHub` in `internal/api/prs_performance_test.go`. It creates an isolated PostgreSQL schema, 15 closed/merged PRs and 3 open PRs without pre-populating `pr_snapshots`. An `httptest` GitHub service sleeps one second for **every** HTTP request. The open PRs use the built-in merge policy (`requireChecks=true`, no required attestation), have required CI checks, and are not drafts, so all three require a readiness decision.

Command:

```sh
TEST_DATABASE_URL='postgres://postgres:pw@127.0.0.1:55450/postgres?sslmode=disable' go test -count=1 -run TestPRListColdAndWarmWithDelayedGitHub -v ./internal/api
```

Observed on 2026-10-20 (local): cold `2.011576462s`, 17 GitHub requests; warm `1.446488ms`, 0 additional GitHub requests. The cold count is one open-list request, one closed-list request, and five concurrent read-only HTTP requests per open PR (live pull, branch protection, branch rules, statuses, check runs). The test asserts cold ≤3 seconds and warm ≤300 milliseconds. GitHub App token minting and repository resolution are outside this fake service; the timing measures the PR API endpoint and its GitHub read calls.

Migration 16 adds `pr_snapshots` for observed PR metadata and readiness. Once a PR is closed, list reads use its persisted title, state, merged flag, closed time, URL, and head. A later reopen webhook restores the open state. `SavePRSnapshot` refuses an older refresh that would overwrite a newer webhook observation.

The list response adds optional per-item `unavailable: true` when one PR cannot be read, with `reasons: ["状态暂时无法获取"]`; other PRs remain in the response. An expired list response adds `stale: true`, returns its previous items immediately, and refreshes in the background. A changed refresh emits a `pr` event from the persisted snapshot, so event hydration does not repeat GitHub requests. Closed PR webhook events also hydrate from the snapshot. `state=open|all` and all existing item fields retain their meanings; `all` still includes only PRs closed in the last seven days.

Focused tests cover terminal reuse, one open/closed bulk page, concurrent projects without a held global lock, same-PR request coalescing (including failure), stale refresh notification, one-item failure isolation, webhook hydration, migration replay, webhook/refresh ordering, base-branch changes, and `head_sha` parsing in both live and list readiness paths.

Full validation command:

```sh
TEST_DATABASE_URL='postgres://postgres:pw@127.0.0.1:55450/postgres?sslmode=disable' go test -race -p 1 ./...
```

The focused race run for `./internal/api ./internal/github ./internal/merge ./internal/router ./internal/store` passed; output is saved outside the worktree at `/tmp/swarmjet-r8-a-focused-race.log`. A full-tree race run is required after the final commit; its output should be saved alongside this record.
