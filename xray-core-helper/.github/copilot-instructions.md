# Copilot instructions for xray-core-helper

Read the workspace and component AGENTS.md instructions. The canonical
architecture is in docs/ARCHITECTURE.md at the workspace root; future work is
in docs/ROADMAP.md. Helper API/native references are in xray-core-helper/docs.
Work within the user's active task and verify its acceptance criteria.

Essential constraints:

- This Go daemon is the sole privileged writer of Xray JSONC and helper state.
- Keep Xray routing operational without the helper; do not move data-plane or
  exporter telemetry behavior into this process.
- Expose only versioned, typed, secret-redacted APIs over a Unix socket.
- Never add raw JSON editing, arbitrary paths/commands/units, shell execution,
  a TCP listener, offline mutations, compatibility wrappers, or a Go
  scheduler.
- Preserve JSONC comments and unknown fields through narrow edits.
- Serialize mutations and enforce generation/hash conflict detection,
  validation, atomic replacement and rollback.
- Use fakes and temporary resources in tests. Never operate on host Xray,
  systemd, privileged paths or runtime databases from tests.
- Keep command packages thin and domain logic in focused internal packages.
- Add tests with behavior and run the validation commands from `AGENTS.md`.

When generating a plan, map every step to the active task and identify
which acceptance criterion it satisfies. When handing off, list exact tests
and unresolved contract questions; distinguish implemented behavior from planned work.
