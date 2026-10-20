# Xray routing exporter agent guide

## Repository boundaries

- Xray, nftables and `xray-tproxy-routing.service` form the independent data plane.
- `xray-core-helper` is the only privileged application and configuration owner.
- `xrayui-web` is an unprivileged helper client.
- This exporter owns routing telemetry, Xray statistics, metrics and notifications.

Read [architecture](../docs/ARCHITECTURE.md) and
[operations](../docs/OPERATIONS.md) before cross-component changes.

The shipped helper profile intercepts selected LAN and server-originated IPv4
traffic. Port/mark/table values are profile defaults, not generic engine policy.
Preserve declared relationships and Xray bypass; never infer network semantics
from exporter telemetry. Do not commit credentials, databases or runtime logs.

## Validation

From this directory:

```bash
go build -trimpath -ldflags='-s -w' -o xray-routing-exporter ./cmd/xray-routing-exporter
go test ./...
```

On the router, also verify:

```bash
systemctl is-active xray-routing-exporter.service
curl --fail --silent http://127.0.0.1:9101/healthz
curl --fail --silent http://127.0.0.1:9101/readyz
curl --fail --silent http://127.0.0.1:9101/metrics | head
```

Keep metric labels low-cardinality. Routing events are not byte counts. The
HTTP endpoint stays loopback-only and the webhook remains on its Unix socket.
Helper metadata must be consumed through its bounded typed API; the exporter
must not discover or write Xray configuration directly.

## Links

- [Installation](../docs/INSTALLATION.md)
- [Xray configuration reference](../home-router/conf/config.json)
- [nftables rules](../home-router/conf/nftables.conf)
- [Exporter API and deployment](README.md)
- [Monitoring deployment](../xray-monitoring-docker/README.md)
