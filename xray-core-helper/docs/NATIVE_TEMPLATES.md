# Native system-template contract

This document describes the implemented binding, candidate and grouped recovery
contract. Xray remains a structured JSONC resource with typed editing APIs.
Native files own their semantics. See [architecture](../../docs/ARCHITECTURE.md)
and [acceptance status](../../docs/TESTING.md) for system boundaries and verified
host results. Reading this contract does not authorize changes to a host.

## 1. Scope and authority

Nft file editing uses textual block replacement:

```nft
# helper:begin gateway
# Native, repeatably applicable nft commands and definitions from the template
# helper:end gateway
```

Replace only the contents of the unique ordered marker pair; preserve everything
outside byte-for-byte. If both markers are absent, ordinary replacement reports
`initialization_required`. An explicit previewed first-install action may append
the template block with both markers, preserving existing bytes and adding a
separating newline if necessary. Empty or absent files are supported; record
original bytes/metadata or absence for rollback. Refresh/startup never inserts
markers. One missing marker, duplicates, nesting or reversed order fail without
writing. Never silently delete/adopt conflicting existing definitions or move
unmarked configuration into the block. Native `nft --check` detects errors it
can validate, not all semantic conflicts or duplicate valid rules. Initializing
a nonempty file requires a trusted operator preview of existing content/diff,
an explicit warning that rule interaction is unchecked and confirmation.
Secret-bearing raw files must not enter ordinary API projections or logs;
use an explicitly authorized local/operator review path. Do not introduce a
rule compatibility analyzer. Whole-file native validation is mandatory but
is not proof of isolation or forwarding compatibility.
Activation still uses confirmed apply and runtime recovery, not merely append.
The template owns native commands for replacing its live objects;
helper must not invent them by interpreting rule structure. The shipped profile supplies repeatable,
atomic native application for absent and existing objects. File replacement,
native loading and confirmed runtime rollback remain distinct operations.

Native files define all rules, ordering, chains, unit commands and sysctl keys.
Binding comments declare only value bindings; block markers delimit text. They
cannot execute commands, choose
paths, grant ownership, select adapters or add mandatory engine policy.
Files without declarations have no helper substitutions or relationship checks.
They still support byte comparison, backups and safe native validation/apply
where the trusted adapter supports their operational scope.

The trusted catalog binds each template ID to fixed destinations, permissions,
validator, adapter and recovery scope. Source content never expands that scope.
Unsupported operation scope is an adapter limitation, not a routing mismatch.

## Factory placeholders and persisted network selection

The shipped nftables/routing-unit templates contain typed placeholders rather
than sample LAN, port, mark or GID values. A factory declaration omits `token`:

```nft
# helper:v1 bind ingress type=interface source=helper.network.interface
define lan_interface = ${lan_interface}
# helper:v1 bind listener type=port source=xray.tproxy-port
define tproxy_port = ${tproxy_port}
# helper:v1 bind intercept type=uint32 source=helper.network.intercept-mark
define intercept_mark = ${intercept_mark}
```

Only one complete unquoted placeholder in the immediately following logical
statement is replaced. The placeholder name is a label; the annotation's trusted
`source` selects the value. There is no shell/environment lookup or global
substitution. Interface names receive native quotes; other values are checked
and encoded by their declared scalar type. Unknown/unresolved sources, unsafe
values and ambiguous spans fail before writing. Unannotated native references
such as `${TPROXY_MARK}` in systemd commands remain unchanged.

Preparation emits concrete native values plus their `token` locators. Installed
system files use the concrete syntax below; native working templates retain
placeholders and are never rewritten by setup. Status compares and validates
disposable rendered candidates against the system files; ordinary
status checks never write templates or apply candidates. Native validators run on prepared
candidates, never directly on unresolved factory templates.

Setup saves the chosen `interface`, `address` and canonical `ipv4_prefix` in
`[network]` of helper's startup TOML. `intercept_mark = 1` and
`policy_table = 100` are configurable kernel-routing defaults in that section.
The section is backed up, committed and restored with the setup file group,
including exact previous bytes and ownership/permissions on recovery. Explicit
native template apply uses the saved network selection, the current Xray TProxy
port/outgoing mark and the resolved Xray service GID. Editing TOML does not
silently apply networking: restart helper to load it, then explicitly prepare/apply
and confirm changes. Xray socket marks and kernel interception marks remain
separate parameters. No port, outgoing mark or GID fallback is inferred from a
placeholder label.

For each declared relationship, the expected helper/Xray/service value is checked
against the actual scalar read from the installed system file. Comparisons use
typed normalized values (for example decimal and hexadecimal marks can be equal).
Missing sources are unresolved; unequal values are mismatches. Reading status
never adopts the system value into helper settings or changes a source template.

## 2. Versioned native comment syntax

Use a standalone native `#` comment immediately before the native logical
statement containing the marked literal:

```text
# helper:v1 bind <id> type=<type> source=<provider> token=<JSON-string>
```

`id` is local to the file: 1-64 ASCII letters/digits/underscore/hyphen, starting
with a letter. It is an identifier, not a variable name or an implied role.
Attributes occur exactly once in the shown order; unknown versions/directives/
attributes fail as invalid declarations. A comment beginning `# helper:` is
reserved, except the exact block delimiters defined above; old factory value
markers must be migrated explicitly, never ignored as
ordinary comments. Other native comments are unchanged.

`token` is a JSON-quoted exact native literal spelling, including any native
quotes. It locates one complete scalar token in the immediately following
logical statement. Matching text inside comments or larger strings is not a
match. No matching token or multiple matching tokens is an error, not a guess.
No occurrence numbers, regex, JSON paths or command substitutions are accepted.
At most one declaration precedes a statement in v1; repeat a native scalar
definition when a format would otherwise need multiple editable values.

The format adapter identifies native logical statements (including native line
continuations) and scalar token boundaries only. It must not require particular
keys, variable/chain names, protocols, rule order or command shapes. Unsupported
literal encoding is explicit and leaves the source unchanged. Unit-specifier,
environment and nft variable expansion are not evaluated by the annotation
reader. Bind the literal definition rather than an expanded reference.

Initial types: `uint32`, `port`, `interface`, `ipv4-prefix`, `bool01`.
Xray JSONC also supports `geosite-reference` with the provider
`helper.user-geosite`, resolved from `[geodata].user_geosite`. Use a `//` comment
and one complete quoted `ext:file:category` locator on the next statement:

```jsonc
// helper:v1 bind user-geosite type=geosite-reference source=helper.user-geosite token="\"ext:geosite_user.dat:USERLIST\""
"ext:geosite_user.dat:USERLIST"
```

Preparation and explicit template apply replace only the filename, preserve the
category, and update the locator. Helper startup does not rewrite Xray. Unmarked
references remain unchanged; renaming the existing asset is the operator's job.
The target file must exist before ordinary template validation. Network setup
prepares a missing initial USERLIST asset and the `proxy-domains-permanent` rule
in its durable transaction, validating against private staged assets first.
The permanent rule anchors runtime dynamic exclusions/additions after special
system rules and before the geosite and remaining user rules.

The scalar renderer also supports JSONC `geoip-reference` (`helper.user-geoip`),
`ipv4-address` (`helper.network.address`), `loopback-endpoint`
(`helper.xray-api`), `log-destination`, `log-level` and `boolean`. Logging providers
are `helper.logging.access-path`, `helper.logging.error-path`,
`helper.logging.level` and `helper.logging.dns-log`; TOML keys are `access_path`,
`error_path`, `level`, `dns_log` under `[logging]`. Defaults are `"none"`, `""`,
`"warning"`, `false`. User geoip filename defaults to `geoip_user.dat` under
`[geodata].user_geoip`. JSONC annotations require a `//` comment and a complete
unquoted placeholder on the following statement. The renderer supplies JSON
quoting for strings; ports/marks remain numbers, booleans remain booleans.
For example:

```jsonc
// helper:v1 bind dns-log type=boolean source=helper.logging.dns-log
"dnsLog": ${dns_log}
```

The output contains the concrete value and a matching `token=` locator. Only
marked scalars change; comments, unknown fields and unmarked strings remain
unchanged. Invalid JSON encoding, unknown/unresolved providers and invalid types
fail before apply, without exposing provider values in errors. No live Xray
port/mark defaults are inferred by this renderer. New geosite/geoip placeholders
select `ext:<configured-filename>:USERLIST`; existing concrete references retain
their category when the filename is rebound.

Explicit apply and read-only status/validation use the rendered candidate.
Concrete rollback evidence is validated as stored, without rebinding to current
settings. Setup candidate preparation also renders these bindings; existing
Xray port/mark values are retained, with initial defaults only for absent fields.
Helper settings changed after preview block preparation. The shipped Xray
`templConf/xray/config.json` uses these bindings. Setup prepares both USERLIST
assets; shared consolidation commits the mixed domain/IPv4 baseline, and typed
logging mutations synchronize TOML and Xray. Loading TOML alone never writes
Xray or creates log files. Server acceptance remains separate from local tests.

`uint32` accepts native decimal/hex spelling and compares numeric values;
`port` additionally requires 1..65535. `bool01` accepts only 0/1 where explicitly
declared. The initial interface literal encoder supports 1-15 ASCII letters,
digits, dot, underscore and hyphen (excluding `.` and `..`); unsupported names
fail explicitly instead of being inserted unescaped. Interface and prefix
validation applies only to declared values, not
to all strings or sysctl settings in a file. No implicit default is supplied.

Initial provider names are trusted, read-only identifiers:

- `setup.interface`, `setup.ipv4-prefix`: the explicit setup selection;
- `helper.network.interface`, `helper.network.ipv4-prefix`,
  `helper.network.intercept-mark`, `helper.network.policy-table`: helper TOML
  network parameters; expected values are compared with actual marked system
  values, including interface/prefix. Missing LAN selection is unresolved;
- `xray.service-gid`: resolved service identity, only if requested;
- `xray.tproxy-port`, `xray.intercept-mark`, `xray.egress-mark`: existing
  structured Xray projections, only when their value is unambiguous;
- `native.<template-id>.<binding-id>`: another explicitly declared value;
- `native.literal`: a native-owned scalar parameter. It is checked for type,
  retained during rendering, and can be referenced by other annotations without
  inventing an Xray property for a kernel-only policy parameter. It introduces
  no implicit mark, table, chain or command semantics.

Provider names do not accept arbitrary paths, unit names or executable options.
Missing/ambiguous Xray values are unresolved, never replaced with 52345/1/255.
Native references must be acyclic and type-compatible. Runtime checking compares
the actual marked system literals against providers; it never silently renders
or adopts files. Setup rendering resolves selected inputs and changes only the
marked token span, retaining its native encoding style where valid. If a setup
binding is checked outside setup, its status is `setup_only`, not a request
to rediscover a possibly different interface. Explicit native references to it
compare the actual marked literal; no runtime setup-provider lookup occurs.

Reader bounds: 1 MiB/file, 256 declarations/file, 4096 bytes/declaration,
64 native-reference edges per resolution chain. Reject invalid UTF-8 in marked
metadata, cycles, duplicate IDs, conflicting spans and unresolved requested
bindings before writing. Errors contain template ID, binding ID and a stable
code, never source lines, raw tokens, provider values or command output.

## 3. Examples (specification fixtures, not shipped migrations)

Nftables: variable names and policy remain entirely native:

```nft
# helper:v1 bind uplink type=interface source=setup.interface token="\"lan0\""
define ingress_device = "lan0"
# helper:v1 bind listener type=port source=xray.tproxy-port token="52345"
define transparent_listener = 52345
# helper:v1 bind packetmark type=uint32 source=xray.intercept-mark token="0x1"
define selected_mark = 0x1
# helper:v1 bind bypass type=uint32 source=xray.egress-mark token="0xff"
define excluded_mark = 0xff
```

Sysctl: this example relates two declared scalars; it imposes no mandatory key
or recommended value on other files:

```ini
# helper:v1 bind forwarding type=bool01 source=native.sysctl.forwarding-default token="1"
net.ipv4.ip_forward = 1
```

The example requires an explicit `forwarding-default` binding supplied in the
same template; without it the declaration is unresolved. A normal unannotated
`net.ipv4.ip_forward = 1` is valid and has no declared equality check.

Systemd: the reader marks a scalar, not an understood routing command:

```ini
[Service]
# helper:v1 bind policymark type=uint32 source=native.nftables.packetmark token="0x1"
ExecStart=/sbin/ip rule add fwmark 0x1 table 100
```

The native reference resolves the marked value in the actual nft system file,
not a constant inferred from the unit command. Other tokens are unmarked and
are not validated as policy by Go. Native unit validation remains mandatory.
Successful value comparison does not prove the command ran or forwarding works.

## 4. Trusted apply/rollback adapters

Every adapter prepares durable evidence before file replacement or runtime
mutation: template revision, original file bytes/metadata or absence, bounded
runtime snapshot, ownership descriptor and prior service state. Capture failure
blocks apply. Evidence is private and uses the existing safe-apply journal,
confirmation timer and boot-recovery mechanism, not a second transaction engine.
The approved system recovery contract uses file evidence and manual reboot,
not live-state snapshots or a promise of immediate runtime restoration.

For nftables the native gateway block supplies its own safe replacement
commands. No separate table-name list in helper settings/catalog is approved.
Text delimiters do not constrain the effects of native commands in the kernel.
Approved recovery correction: no nft object inference, snapshot table list or
automatic full-ruleset restoration. On rollback restore the prior file and
wait for a MANUAL SERVER REBOOT. Do not reboot automatically, reload the old
file as a supposed runtime rollback, or report active rules as restored.

Load the intended native replacement as one transaction; validate the exact
candidate first. Replacement commands live in the template, not a generated
Go wrapper. No global `flush ruleset` or separate delete-then-load fallback is
approved. A native validation success alone is not proof of effect isolation
or forwarding compatibility. Preview must warn about manual reboot before a
potentially disconnecting apply; access may require an independent console.

File restoration records `awaiting_reboot` durably with the current Linux boot
ID and keeps the transaction/backup pending. The API exposes `rebootRequired`,
readiness reports `reboot_required`, and mutations are gated until recovery.
Web displays a global red non-dismissible alert; CLI writes an explicit warning.
Restarting helper/web does not clear the state. After a later boot is observed,
finish bookkeeping; boot-ID change is not itself proof of healthy forwarding.
If a crash interrupts file restoration, restore it first and conservatively
require a subsequent reboot rather than assume this boot loaded the old file.

Approved recovery group: nftables, sysctl and the managed systemd unit belong
to ONE system recovery operation. Capture all member files (including original
absence and metadata) before the first mutation, even when applying one selected
template. Keep per-template apply/confirmation choices, but never create three
independent rollback/reboot cycles or combine backups from different operations.
Xray structured configuration is outside this recovery group.

On timeout, explicit rollback or interrupted unconfirmed apply, restore every
group member to that operation's baseline, then enter one awaiting_reboot state.
Do not reload nft, write old live sysctl values or stop/start a restored unit as
a substitute for reboot. Applying still uses fixed-purpose trusted adapters;
annotations never supply executable recovery commands. Daemon confinement must
not be silently broadened. Unit commands remain opaque to helper.

Multi-file restoration is not filesystem-atomic: journal each member durably,
retain all baseline backups and retry interrupted restoration idempotently.
Only report files_restored after every member is restored. A partial restore
stays recovery_required and blocks writes; it must not suggest reboot is ready.
The boot identity used to finish recovery must be recorded after all files are
restored, so an intervening reboot cannot falsely complete a partial operation.
After a later boot, check the files against the baseline and report component
readiness separately; a new boot ID alone does not prove successful recovery.

Implementation stores the immutable complete group in `safe_apply_baseline`,
with per-member restoration progress in `safe_apply_restore`, in the existing
private SQLite database. A retry re-restores every member and checks the complete
baseline instead of trusting old progress. Recovered-file drift after a new boot
returns the operation to `rolling_back`. Missing historical group evidence fails
closed; normal status/recovery APIs remain available, writes stay blocked.
Terminal snapshots follow the configured retention (default 10 operations),
while all pending evidence remains protected.

Reboot restores the prior boot configuration, not arbitrary irreversible unit
side effects (deleted files, remote writes or migrated data). Those are outside
the guarantee. Runtime effects can remain changed until the manual reboot.
Failed rollback retains artifacts and blocks conflicting writes; syntax success
or a returned HTTP response cannot mark incomplete recovery as confirmed.

## 5. Status and implementation gates

Distinguish byte drift, declared-value mismatch, invalid declaration, unresolved
provider, unsupported adapter scope, unchecked runtime effect and failed actual
connectivity. No declarations means `not_declared`, not degraded configuration
or proof of health. Runtime probes are catalog-bound observations; annotation
comments cannot define executable probes.

Bindings, candidate preparation, native validation and grouped recovery have
hermetic coverage. Structural nft/unit/sysctl policy parsers and a generated Go
nft wrapper are not part of the engine. The dedicated sysctl writer has writable
/proc/sys scope, including non-network tunables, with a fixed executable and
configuration path. The daemon retains its kernel-tunable protection.
Native syntax success does not prove live effects. Actual forwarding, failure
and reboot acceptance are tracked in [TESTING](../../docs/TESTING.md).

### Backup generations

`GET /v1/templates/{id}/backups` exposes `generation` (the greatest generation
of any backup record, used for backup restore) and `completedGeneration` (the
greatest completed generation, or zero, used for apply and accept-system).
Rollback/pending records remain visible and keep their generation numbers, but
do not advance `completedGeneration`. Clients must use the generation associated
with the requested operation; the T8 runner and web panel follow this contract.

### User assets during setup

Setup also stages the configured user geoip file when referenced by its Xray
candidate. An absent file gets one empty USERLIST category, without a sentinel IP;
an existing file is preserved. User geosite and geoip share the durable setup
manifest and private validation projection. Live/candidate hashes are checked
before publication, and complete asset drift checks precede rollback writes.
Shared domain/IPv4 consolidation is a separate remaining integration step.
