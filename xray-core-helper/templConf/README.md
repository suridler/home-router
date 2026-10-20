# Native configuration templates

These files are secret-free native configuration templates. Nftables and the
routing unit use explicitly annotated `${name}` placeholders. Helper prepares
concrete native candidates before nftables/systemd validation; unresolved
factory templates must not be loaded directly. Managed web
settings are helper `[web]` settings, not a native template.

## Installed Xray template

[`xray/config.json`](xray/config.json) is the canonical installation template,
based on the effective working-server policy reviewed on 2026-10-19. It contains
annotated scalar placeholders; setup and explicit apply render separate concrete
candidates. Do not load it directly or substitute through a shell. Setup stages
both USERLIST assets before validation. Shared consolidation commits both assets
and the domain/IPv4 baseline together.

The template preserves the reviewed gateway policy and explanatory comments.
`direct` remains the first/default outbound. Initial `proxy` uses the same freedom
settings as `direct`; remote VLESS credentials/transport are absent. Existing
configured proxy outbounds must be retained when adopting/preparing an existing
installation, not replaced with this initial freedom outbound.

### Routing and DNS policy

- Keep `routing.domainStrategy = "AsIs"` and TProxy sniffing `routeOnly = true`.
  Sniffed names select rules; the original destination IP remains the connection
  target. No extra routing-stage DNS lookup is implied.
- Preserve the system prefix: LAN DNS, internal proxy DNS, BitTorrent direct,
  and private-IP direct. Insert the dynamic API overlay immediately before
  `proxy-domains-permanent`, followed by `proxy-ip-permanent`, then ad blocking
  and the remaining direct/proxy policy. Explicit user choices override those
  following category rules, but not the preceding system rules.
- Cloudflare `domains` contains exactly the domain conditions of
  `proxy-domain-ru-blocked` plus the bound permanent USERLIST reference. Current
  categories: `ru-blocked`, `ru-blocked-all`, `google`, and `telegram`.
- Cloudflare DoH uses `https://cloudflare-dns.com/dns-query`, tag `dns-proxy`,
  routed through outbound `proxy`. `skipFallback = true` keeps it out of fallback
  for other domains; `finalQuery = true` prevents direct DNS fallback after a
  selected Cloudflare query fails. Keep sequential queries; no DNS racing.
- Other names use the retained Yandex local-mode DoH, then system DNS fallback.
  `UseIPv4` remains the DNS query strategy. Bootstrap hosts remain those in the
  template; changing resolvers or adding a proxy-server bootstrap policy requires
  a separately reviewed change.
- The template does not enforce client DNS use, intercept all external DNS, or
  prevent client DoH/DoT and IPv6 bypass. Cloudflare through initial freedom is
  encrypted DoH with direct egress, not a remote proxy privacy guarantee.
- Individual domains stay in their temporary rule until their transfer into
  permanent USERLIST is explicitly performed and verified after release.
  Do not duplicate that temporary list in DNS. Individual IPs remain unchanged.
- Dynamic additions/exclusions change runtime routing only. Before consolidation,
  additions are not automatically covered by USERLIST DNS; exclusions can still
  use Cloudflare while traffic goes direct. Overlapping earlier direct rules can
  also differ from DNS selection. Record these limits rather than claiming full
  DNS/routing equivalence. IP/protocol/port rules cannot be mirrored as a domain
  DNS list before resolution.
- Routing webhooks are omitted; they do not provide traffic volumes per client.

### Binding contract (renderer and setup preparation implemented)

The typed scalar renderer is wired into setup, explicit template apply,
read-only comparison/validation and backup validation. All setup working copies,
including Xray, retain their bytes and metadata. Preparation renders the selected
network and helper settings, retains existing Xray port/mark values, and rejects
settings drift after preview. Initial port/mark defaults apply only when those
fields are absent during setup. Typed logging mutations synchronize TOML and
Xray transactionally; startup never applies configuration.

Each annotation ID is unique within the template; one provider/placeholder may
be reused at several explicitly annotated locations. Render typed JSON scalars
only at declared locations, never shell/environment or unrestricted text
substitution. The source template and working copy remain byte/metadata stable;
prepare/apply produces separate candidates. Startup and refresh never apply
configuration. Compare rendered expected values against actual system values
and report drift without adopting system values automatically.

| Placeholder | Source | Initial value / encoding |
| --- | --- | --- |
| `lan_address` | `helper.network.address` | Selected LAN IPv4 address, JSON string |
| `tproxy_port` | `xray.tproxy-port` | Existing Xray port; initial default `52345`, JSON number |
| `egress_mark` | `xray.egress-mark` | Existing Xray outgoing mark; initial default `255`, JSON number |
| `xray_api` | `helper.xray-api` | Helper `[xray].api`, default `127.0.0.1:10085`, JSON string |
| `user_geosite_reference` | `helper.user-geosite` | Complete `ext:<user_geosite>:USERLIST` JSON string, identical in DNS and routing |
| `user_geoip_reference` | `helper.user-geoip` | Complete `ext:<user_geoip>:USERLIST` JSON string, filename default `geoip_user.dat` |
| `access_log_path` | `helper.logging.access-path` | `"none"` (disabled); empty string means stdout; an absolute path selects a file |
| `error_log_path` | `helper.logging.error-path` | `""` (stdout, normally systemd journal); `"none"` disables; an absolute path selects a file |
| `log_level` | `helper.logging.level` | `"warning"`, JSON string |
| `dns_log` | `helper.logging.dns-log` | `false`, JSON boolean |

Logging settings belong in helper text configuration. `log-destination` accepts
Xray special values or validated absolute file paths, not just paths. Create
file destinations and service permissions only when file logging is selected;
stdout/default logging must not require private log files. Preserve helper error
history collection when using journal. Helpers' typed logging mutations and
prepare/apply must keep these declared settings consistent, rather than silently
restoring defaults on the next apply. No automatic startup mutation is allowed.

### User domains and IPv4 resources

Reuse the existing resource-addition workflow for both domains and IPv4 addresses.
Parse and validate the input to distinguish a domain from an IPv4 address; store
and apply each as its actual type. Keep existing domain normalization/validation.
Domains populate `geosite_user.dat:USERLIST`; IPv4 addresses populate
`geoip_user.dat:USERLIST`. Both filenames are helper settings and bind during
explicit preparation/apply. Do not create a second unrelated IP-management flow
or a one-off legacy import. Preserve the existing desired/baseline, additions,
exclusions, consolidation and restart-reconciliation behavior for both types;
runtime IP rules must use Xray `ip` conditions, not domain strings.

The permanent IP rule remains alongside permanent domains before category rules.
IP entries do not go into DNS `domains`; they cannot select a resolver before
name resolution. Setup prepares a valid initially empty USERLIST geoip category.
The empty and populated encodings were accepted by an isolated Xray 26.3.27
`run -test` using synthetic config/assets; retain this integration check for
other supported Xray versions. Do not invent a matching placeholder IP to bypass
validation. IPv6/CIDR behavior is not defined by this decision; clarify before
extending input support beyond IPv4 addresses.

### Integration acceptance

Installation and acceptance checks:

1. Implement/validate all declared providers and types, including geoip.
   Preserve comments/unknown fields.
2. Resolve live port/mark from existing Xray and selected network from helper;
   initial defaults apply only when building missing initial settings. Preserve
   configured remote outbounds and unrelated existing settings during preparation.
3. Supply valid assets/categories before `xray run -test`: main geosite/geoip,
   permanent USERLIST, and managed user geoip. Check missing category/file, service
   read permissions, LAN port 53 conflicts, and isolated clean-install preparation.
4. Verify both USERLIST occurrences bind together, annotation IDs are unique,
   category sets agree, no placeholder remains, and rule order survives setup,
   repeated apply, runtime overlay rebuild, and restart recovery.
5. Verify Cloudflare-selected query failure does not reach direct DNS, while
   ordinary names retain direct DNS. Exercise this with a real Xray and isolated
   fake upstreams; JSON parsing alone does not prove DNS behavior.
6. Verify template immutability, rendered-vs-actual comparison, explicit changes
   and recovery with file metadata/assets, plus preservation of journal errors.
   Document runtime DNS lag and the temporary individual-domain exception.


The repository tree is package input only. Installation copies missing files
to the private working tree `/etc/xray-core-helper/templConf` and never edits
this checkout at runtime.

| Template ID | Repository file | Installed working file | System file | Working-template owner/mode |
| --- | --- | --- | --- | --- |
| `xray-config` | `xray/config.json` | `xray/config.json` | Effective `xray.service` config path (normally `/usr/local/etc/xray/config.json`) | `root:root`, `0600` |
| `nftables` | `nftables/nftables.conf` | `nftables/nftables.conf` | `/etc/nftables.conf` | `root:root`, `0600` |
| `tproxy-unit` | `systemd/xray-tproxy-routing.service` | `systemd/xray-tproxy-routing.service` | `/etc/systemd/system/xray-tproxy-routing.service` | `root:root`, `0600` |
| `sysctl` | `sysctl/99-xray.conf` | `sysctl/99-xray.conf` | `/etc/sysctl.d/99-xray.conf` | `root:root`, `0600` |

The modes above describe private working templates, not necessarily the modes
required by the destination service. Component appliers preserve or install
the explicitly validated destination metadata.

The Xray destination uses root ownership and the discovered service group with
mode `0640`; the other system destinations use
`root:root 0644` (nft/sysctl/unit).
Rollback restores the original mode and UID/GID, not the installation defaults.
These defaults are not security-scanner criteria: accessible existing system
files are not rejected because of administrator-selected owner/group/mode, and
helper does not automatically repair them.

The nft template uses standalone `# helper:v1 bind` comments before native
scalar definitions. Their local IDs and native variable names are not engine
requirements: setup substitutes only declared tokens. Unannotated templates
remain unchanged without requiring three setup markers. GID resolution during
candidate preparation runs only for a declared binding or Xray's own destination
permissions; no developer-host GID is copied. Literal and locator metadata are
updated together in candidates/system files. Setup preserves all working
template bytes and metadata; status compares a disposable rendered candidate.

The `# helper:begin gateway` / `# helper:end gateway` pair delimits the intended
text block. The bounded `nativeblock` builder is integrated into setup and
confirmed apply. Contents outside the system markers are preserved; unmarked
files require explicit reviewed initialization. Native add/delete/recreate
commands live in the factory template, not a Go wrapper. Runtime loads the
managed block only after native validation; no semantic compatibility is inferred.
The baseline preserves LAN plus loopback ingress and OUTPUT marking. Local and
reserved destinations, Xray's egress mark/GID and container ingress bypass
interception. `proxy_clients` is declared but does not add a source filter.
Port and outgoing mark come from Xray; GID comes from its service. LAN selection,
interception mark and policy table come from helper `[network]` (mark/table
default to 1/100). Only explicitly annotated relationships are validated. See
[native contract](../docs/NATIVE_TEMPLATES.md) and
[acceptance matrix](../../docs/TESTING.md).

Hermetic validation of templates and preparation:

```bash
go test ./internal/nativebinding ./internal/setup ./internal/templateapply ./packaging
```

The sysctl template contains assignment lines accepted by `sysctl --system`;
it must not be applied merely to validate repository content. The helper uses
capability-checked `sysctl --dry-run --load` validation.
Missing dry-run support fails explicitly. The approved dedicated sysctl oneshot
may write /proc/sys, including non-network tunables, using only the fixed
`/usr/sbin/sysctl -p /etc/sysctl.d/99-xray.conf` invocation. The daemon retains
ProtectKernelTunables. Actual permissions/effects still require T8 host acceptance.

The shared setup engine records selected interface/address/prefix in helper
`[network]` and renders their declared native fields transactionally. Native
validators check the prepared files; ordinary status checks never rewrite them.

The installation Xray template preserves distinct `proxy` and `direct` tags.
Initially both use freedom with the same egress bypass mark. Private routes
remain direct; the transparent ingress default selects proxy. Changing proxy to
an actual remote outbound retains routing tags and must retain its bypass mark.
A tag called proxy is not proof of a configured or reachable proxy server.

The persistent nft file has empty hooked `prerouting`/`output` chains and
separate `prerouting_proxy`/`output_proxy` processing chains. Loading it alone
leaves interception off. The standalone `/usr/local/libexec/xray-tproxy/network`
loads the marked native block and sysctl, installs the exact policy route/rule,
and activates both jumps atomically as its last step. Disable flushes both entry
chains first, then removes the matching rule/route without flushing other routes.

Xray's separate `50-tproxy.conf` invokes repeatable cleanup before start, during
stop and after exit. Post-start creates `/run/xray-tproxy/started` and restarts
the routing unit only if enabled; failure makes Xray start fail. The routing
unit requires the marker and has no After/BindsTo/PartOf edge to Xray. No helper
CLI/config/DB/API/socket participates; `network-check` is explicit diagnostics.
Manual nft reload requires explicit routing-unit restart to restore interception.

Preparation or TProxy-unit apply installs script/drop-in/independent sysctl unit
from fixed factory assets. Bundle updates refresh only factory sources, not
active lifecycle files; helper uninstall preserves the native installation.
All six native files share recovery evidence; working templates remain unchanged
until explicit operator action. Actual boot/crash/LAN behavior requires operator
acceptance. See [native lifecycle/recovery](../docs/NATIVE_TEMPLATES.md).

### USERLIST asset preparation

For a candidate referencing the configured `geodata.user_geoip`, setup preserves
an existing file or stages a GeoIPList with one empty `USERLIST` category. It
contains no fabricated IP. Non-empty encoding accepts canonical IPv4 addresses
only, stored as `/32`; domains, IPv6 and CIDR input are rejected. The wire format
follows [Xray-core GeoIP/CIDR definitions](https://github.com/XTLS/Xray-core/blob/v26.3.27/app/router/config.proto).

The same setup manifest covers user geosite, user geoip, Xray/native files and
helper network TOML. Validation uses a private asset directory before publication.
Both live files and all new candidates are checked before the first publication.
Rollback verifies the complete pair before restoring config/removing new assets;
external drift keeps recovery evidence and blocks partial restoration. Existing
user assets are never regenerated or removed by setup. This prepares installation
assets. CLI/web accept domains and IPv4 through one input. Shared consolidation
publishes both files and commits the mixed baseline together; restart reconciliation
restores the separate domain/IP runtime rules.

The opt-in compiler integration test uses `XRAY_HELPER_TEST_XRAY_BINARY` pointing
to a disposable executable copy, and only temporary synthetic config/assets:

```bash
XRAY_HELPER_TEST_XRAY_BINARY=/absolute/path/to/disposable/xray \
  go test ./internal/usergeoip -run TestEncodedAssetsWithXray -v
```

Empty, single-address and multiple-address USERLIST files passed with Xray
26.3.27. This compiler check does not prove forwarding or replace server T8.
