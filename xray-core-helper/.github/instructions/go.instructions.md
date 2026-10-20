---
applyTo: "**/*.go"
---

# Go implementation instructions

- Follow `AGENTS.md`, the active task and the current API/native contracts
  in `docs/`. The workspace roadmap describes future work.
- Pass `context.Context` into blocking or external I/O.
- Keep HTTP and JSON concerns at the API boundary; domain packages return
  typed results and errors.
- Inject command, filesystem, clock, Xray API and systemd boundaries where
  failure-path testing is required.
- Execute fixed binaries with argument vectors and no shell.
- Do not expose secret-bearing source text or command output in errors.
- Use strict request decoding and stable error codes.
- Prefer table-driven tests, `t.TempDir`, `t.Cleanup` and deterministic fakes.
- Format changed Go files and run focused tests, then the full validation set.
