# Xray Core Helper Agent Guide

## Start here

This repository is the privileged local control plane for the Xray gateway.
Before changing code, read:

1. `../docs/ARCHITECTURE.md` for architecture and security boundaries.
2. `README.md`, `docs/API.md` and `docs/NATIVE_TEMPLATES.md` for implemented contracts.
3. `../docs/ROADMAP.md` for future work, distinct from current behavior.

Work within the user's active task and its acceptance criteria. Inspect the
implementation before changing a public contract.

## Non-negotiable architecture

- The single config selected by effective `xray.service` ExecStart is canonical
  (normally `/usr/local/etc/xray/config.json`). The helper is its only writer.
  Do not add an independent TOML path or a fallback when discovery fails.
- Helper SQLite stores only state Xray cannot represent. Never store a second
  complete copy of the Xray config.
- `xray-core-helperd` owns ordinary writes. The standalone installation/setup/
  recovery mode of `xray-core-helper` is the explicit exception: stop the daemon
  and acquire the common runtime lock before writing; release it before restart.
  All other CLI commands remain clients of the versioned Unix-socket API.
- Do not add a TCP listener, a general-purpose offline editor, a generic JSON patch
  endpoint, arbitrary command execution, or arbitrary filesystem paths.
- Existing routing must continue when the helper is stopped. The helper is not
  part of the packet data plane.
- `xray-routing-exporter` remains an independent observability component. Do
  not move webhook, telemetry SQLite, retention, or Prometheus behavior here.
- Systemd owns service lifecycle and the independent safe-apply rollback timer.
  The helper's internal scheduler owns error collection, runtime routing
  reconciliation and configured daily geodata updates. Keep rollback independent
  of the daemon and never add per-tick success logging.
- There is no compatibility phase: do not preserve or recreate the old
  `xray-proxy-domain` interface or legacy text-list storage.

## Security rules

- Treat all Xray configuration and command output as secret-bearing.
- Never return or log raw config, UUIDs, passwords, private keys, tokens,
  Reality short IDs, full VLESS URIs, or secret headers.
- Use typed request/response structures, strict decoding, bounded bodies and
  stable machine-readable error codes.
- External processes must use `exec.CommandContext` with a fixed executable
  and argument vector. Never invoke a shell.
- Allowlist service names, actions, managed paths and remote origins.
- Configuration mutations require the appropriate expected generation, are
  serialized, and fail closed on unrecognized external config drift. Follow
  each API contract; setup/probe/service actions do not all share this field.
- Preserve JSONC comments and unknown fields with narrow edits. Never rebuild
  the complete document for a small mutation.
- A failed validation, restart or commit must leave or restore a complete
  previously valid configuration.
- Tests must use temporary files, fake command runners and temporary sockets;
  they must not touch host Xray, systemd, `/etc`, `/usr/local`, or runtime DBs.

## Go structure and style

- Keep both commands thin: orchestration belongs in focused `internal`
  packages.
- Put transport-neutral API contracts in `internal/api`; do not leak HTTP
  objects into domain packages.
- Define interfaces at the consuming package for filesystem, clock, command,
  Xray API and systemd boundaries when tests need substitution.
- Accept `context.Context` for I/O and potentially blocking work.
- Wrap errors with operation context while keeping client responses redacted.
- Prefer standard-library facilities unless a dependency materially reduces
  risk or complexity. Explain every new dependency in the handoff.
- Run `gofmt`; use conventional Go naming; keep exported surfaces minimal.
- Table-driven tests are preferred for validation, transaction phases and
  failure injection. Use `t.TempDir()` and `t.Cleanup()`.
- Do not weaken, delete or rewrite unrelated tests to make a task pass.

## Required workflow

Before editing:

1. Confirm the current task and its acceptance criteria.
2. Inspect the relevant implementation and tests; do not assume the roadmap
   describes already completed work.
3. Check `git status` and preserve unrelated user changes.

During implementation:

1. Make the smallest coherent change that completes the active task.
2. Add or update tests with the behavior.
3. Keep documentation synchronized when a public contract changes.

Choose validation according to the changed files. For Go implementation,
Go tests under `cmd`/`internal`, or module dependency changes, run the following
from this repository root, sequentially with the same persistent Go caches:

```bash
gofmt -d ./cmd ./internal
go test ./...
go test -race ./...
go build ./cmd/...
go vet ./...
```

`gofmt -d` must print nothing. Do not start duplicate full suites, race tests,
build and vet concurrently while compiling a cold cache. If Unix-socket or race
tests fail with sandbox errors such as `operation not permitted`, stop the
superseded run and rerun the same command in a normal environment before
classifying it as a code failure. Never hide a genuine test failure behind an
environment explanation.

For packaging/unit asset changes, run `go test ./packaging` and relevant script
syntax/fixture tests. For host harness changes, run their hermetic fixture tests;
the T8 runner uses `python3 -B -m unittest discover -s packaging -p test_t8_server_test.py`.
Live host execution remains a separate explicitly authorized acceptance step.

For documentation-only or Codex settings/instruction changes, run:

```bash
git diff --check
```

Also parse/load changed configuration files. A docs/settings-only change does
not require the Go build/test/race/vet matrix. For mixed changes, run the union
of the relevant checks.

## Cross-workspace boundaries

This project lives in a multi-root workspace. Change sibling projects only
when the active task explicitly requires integration:

- `../xrayui-web`: unprivileged UI and helper client;
- `../home-router`: gateway units, persistent routing and deployment runbook;
- `../xray-routing-exporter`: independent observability plane;
- `../xray-monitoring-docker`: Prometheus and Grafana deployment.

Before changing gateway behavior, read `../docs/ARCHITECTURE.md` and
`../docs/OPERATIONS.md`. Xray remains a structured
managed config with typed APIs and safe JSONC editing. System templates
(nftables/sysctl/systemd) own their semantics; do not impose rule structure,
chain/table names, command shapes or mandatory key/value sets. Only explicit
annotations authorize substitutions/relationship checks. Keep native validation,
trusted fixed-purpose adapters, backups and confirmed rollback. Never replace
removed semantic checks with arbitrary shell execution. Profile-specific traffic
tests may know a shipped template's behavior; the generic engine must not impose
it on other valid templates. Structural policy parsers and a Go-generated nft batch
wrapper are outside the native contract. System rollback restores one durable nft/sysctl/unit file
group and waits for manual reboot; do not add component live rollback. Preserve
read/status access and the mutation gate when recovery evidence is incomplete.

## Handoff format

Report:

- the completed task and outcome;
- files and public contracts changed;
- tests run with exact results;
- security or recovery assumptions;
- remaining acceptance criteria or blockers;
- unrelated pre-existing worktree changes left untouched.
