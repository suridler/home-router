---
applyTo: "**/*_test.go"
---

# Go test instructions

- Tests must be hermetic and must not read or modify the host Xray config,
  systemd, `/etc`, `/usr/local`, live Unix sockets or runtime databases.
- Use temporary directories and injected fakes for every privileged boundary.
- Assert both success state and rollback state for mutation failures.
- Assert that serialized responses and errors do not contain fixture secrets.
- Cover malformed input, unknown fields, limits, cancellation and stale
  generation behavior where applicable.
- Do not skip security, race or recovery tests merely to make CI pass.
