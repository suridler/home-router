# Home-router workspace instructions

## Scope and communication

- This is one workspace with five components. Read the component's `AGENTS.md`
  and relevant implementation before editing; preserve unrelated worktree changes.
- Respond in Russian unless requested otherwise. State the result, relevant
  verification and remaining limitations concisely.
- If a decision is ambiguous, ask the user before implementing the affected
  behavior. Continue independent work while awaiting clarification.
- Do not perform non-obvious corrective actions without a user request. Report
  configuration limitations explicitly rather than silently rewriting values,
  changing service restrictions, or trying to handle every possible host setup.
- Complete authorized work without repeated confirmation. Use subagents or
  Copilot delegation only when explicitly requested for the task.
- Distinguish this development host from a remote test server. Verify paths,
  installed versions, interfaces and service state on the host being tested.

## Component ownership

- `xray-core-helper`: privileged API, Xray configuration transactions, native
  template application, safe-apply/recovery, proxy domains and geodata.
- `xrayui-web`: unprivileged UI and typed helper API client. Start with
  its `README.md` and workspace `docs/ARCHITECTURE.md` for web changes.
- `home-router`: host configuration, data-plane assets and operational runbooks.
  Read `docs/ARCHITECTURE.md` and `docs/OPERATIONS.md` before gateway changes.
- `xray-routing-exporter`: independent telemetry, statistics and metrics.
- `xray-monitoring-docker`: monitoring deployment.
- Keep changes within the requested component unless integration is necessary.

## Configuration and recovery

- The single config selected by effective `xray.service` settings is canonical;
  helper is its only application writer. Preserve JSONC with narrow edits and protect
  DNS/TProxy rules. Web/exporter consume typed helper projections.
- Native nftables/sysctl/systemd templates own their semantics. Apply only
  explicit bindings with fixed native validators and trusted adapters.
- System rollback restores the durable nft/sysctl/unit file group and waits
  for manual reboot. Helper restart is not reboot; verify a new boot ID and
  matching baseline files. Never clear pending evidence to make a test pass.
- Keep Xray restart explicit. Installing/upgrading services, applying templates,
  changing live routing, geodata updates and failure injection need task scope
  that authorizes those host actions. Host access alone is not authorization.
- Never automatically reboot or run `wsl --shutdown`. Keep independent console
  access for operator-run network acceptance. Do not equate HTTP success or
  active services with working LAN forwarding.
- Never print/commit raw Xray config, credentials, VLESS links, bootstrap tokens,
  runtime databases or backup archives. Keep recovery artifacts private.

## Efficient verification

- Choose checks according to the changed files. Documentation and Codex settings
  require `git diff --check` plus configuration parsing/loading when applicable;
  they do not require rebuilding every component.
- Web code: `npm test` in `xrayui-web`; run `npm ci` only when dependencies need
  installation or the lockfile changed. Run host integration only in an authorized
  host-test task and report sandbox/access skips separately from app failures.
- Helper Go code: follow the component's Go checks. Host harness changes use
  syntax checks and their hermetic fixture tests; see the T8 guide for live runs.
- Run heavy Go checks sequentially with the same persistent `GOCACHE` and
  `GOMODCACHE`. Do not run duplicate full suites, race tests, builds and vet
  concurrently while compiling a cold cache.
- If sandbox/cache/socket restrictions prevent tests from running, use a narrow
  authorized retry in the normal environment. Stop the superseded run before
  retrying, retain genuine failures, and do not disable confinement globally.
- For T8 on an already installed disposable host, use
  `xray-core-helper/packaging/t8-server-test.py` and its companion
  `T8_MANUAL_SERVER_TEST.md`; do not reinstall just to repeat acceptance.
- Report executed checks and actual outcomes. A prepared harness is not a
  successful remote-host run; manual GUI/LAN/nft/crash acceptance stays explicit.
