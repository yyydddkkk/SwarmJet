# Round-5 console screenshots

Captured by `npx playwright test` using the HTTP mock fixtures. The wizard uses
1440×900; Queen, methodology and settings pages use 1440×1000 (full-page capture).

| Files | Coverage |
|---|---|
| 01–03 | Repository → credential-backed Queen model → concurrency; Queen defaults on |
| 04 | Missing credentials with a link to the Models page |
| 05–06 | Queen conversation, provenance, literal HTML text and disabled-state settings link |
| 07–09 | Uninstalled catalog, installation confirmation and installed builtin source |
| 10 | Saved Queen model, concurrency and switch; methodology limits are read only |

Reproduce with `npx playwright test tests/create-hive-round5.spec.ts tests/queen-round5.spec.ts tests/methodology-round5.spec.ts tests/settings-round5.spec.ts`.
