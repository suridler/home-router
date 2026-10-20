# xrayui-web Bruno collection

Open this directory as a collection in Bruno and select the `local`
environment.

Environment variables:

- `baseUrl` — xrayui-web address, default `http://127.0.0.1:3000`;
- `domain` — ordinary DNS name used by add/delete requests;
- `ipv4` — single IPv4 address used by add/delete requests, default
  `192.0.2.1`.

Suggested verification sequence:

1. `01 Read / Health`
2. `01 Read / Proxy Domains`
3. `02 Proxy Domains / Add Domain`
4. `01 Read / Proxy Domains`
5. `02 Proxy Domains / Add IPv4`
6. `01 Read / Proxy Domains`
7. `02 Proxy Domains / Delete Domain`
8. `01 Read / Proxy Domains`
9. `02 Proxy Domains / Delete IPv4`
10. `01 Read / Proxy Domains`

The add and delete requests use one `domain` field/path parameter for both
types. A successful response contains the canonical `domain`, `applied` and the
updated `snapshot`; `applied` describes the runtime overlay, not the USERLIST
files. Values rejected by helper normalization (empty input, IPv6, CIDR, URL or
invalid IPv4) return an error and are not written.

If a mutation response is lost, times out or reports
`mutationMayHavePersisted`, run `01 Read / Proxy Domains` to inspect the current
state. Do not automatically repeat POST/DELETE; after fixing the configuration,
apply the current desired list explicitly with helper/CLI `proxy-domain apply`.
A failed list read does not make the previous snapshot current.

`03 Geodata / Run Geodata Update` is intentionally separate because it
downloads data and restarts Xray. Mutation requests require the server to run
with helper `[web].control_enabled = true`.

The application currently has no HTTP authentication. Keep `baseUrl` on a
loopback; use an SSH tunnel for remote access. Public exposure is not part
of the current beta contract. The collection checks routes and JSON contracts;
a successful run against a host is not a substitute for server acceptance in T8.
