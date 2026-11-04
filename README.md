# nimtel

Shared OpenTelemetry setup for Nimtech Go services. One call installs traces, metrics and logs against the OTLP collector and a `slog` default that writes to stdout as well. Where the collector is comes from the standard `OTEL_EXPORTER_OTLP_*` variables, which the Container Apps environment agent injects, so a service configures nothing beyond its name, version and environment.

## Installation

```
go get github.com/nimling/nimtel@v1.0.0
```

## Wiring a service

```go
shutdown, err := nimtel.Init(ctx, nimtel.Config{
    ServiceName:    os.Getenv("OTEL_SERVICE_NAME"),
    ServiceVersion: os.Getenv("APP_VERSION"),
    Environment:    os.Getenv("SAUTH_ENVIRONMENT"),
})
if err != nil {
    log.Fatal(err)
}
defer shutdown(context.Background())

db := bun.NewDB(sqldb, pgdialect.New())
db.AddQueryHook(nimtel.QueryHook("bookable"))

client := &http.Client{Transport: nimtel.Transport(http.DefaultTransport)}

e.Use(smw.TelemetryMiddleware())
```

1. `Init` installs the providers, the propagator and the `slog` default. Call it first in `main`, before the database and the echo server.
2. `QueryHook(dbName)` is the query hook, built on `bunotel`: a client span per query named after the operation, only when the context already carries a valid span, so a background loop outside a request makes no trace. The span carries `db.name`, `db.operation`, `db.system`, `db.rows_affected` and `db.statement`, the query template without arguments cut to 512 bytes. Errors other than `sql.ErrNoRows` and `sql.ErrTxDone` are recorded on the span. The `go.sql.query_timing` histogram and the `database/sql` pool stats are dropped unless `Config.QueryMetrics` is true.
3. `Transport(base)` wraps a round tripper with `otelhttp.NewTransport`: a client span per outgoing request, the `http.client.*` metrics, and `traceparent` and `baggage` injected so the next service continues the trace. A nil base is `http.DefaultTransport`.
4. The echo server middleware is `smw.TelemetryMiddleware()` from `samna_auth_middleware`, which also stamps every request with the actor's claimius root and owner organization through `nimtel.Organization`. It is not duplicated here.
5. The shutdown function flushes and then shuts down the trace, metric and log providers with the caller's context. Give it a context with a deadline on a graceful stop.

Without `OTEL_EXPORTER_OTLP_ENDPOINT` set, as on a laptop, only the stdout logger is installed and the hook and transport record into the noop providers.

## Config

| Field | Resource attribute | Meaning |
|---|---|---|
| `ServiceName` | `service.name` | Required; `ErrServiceName` without it |
| `ServiceVersion` | `service.version` | The released tag of the service |
| `Environment` | `deployment.environment.name` | `dev`, `tst` or `prd` |
| `Debug` | | Colour handler at debug level on stdout instead of JSON at info |
| `WithoutMetrics` | | Skips the runtime and host feeds |
| `QueryMetrics` | | Exports the `go.sql.query_timing` histogram and the `database/sql` pool stats of `QueryHook` |

`service.instance.id` is `CONTAINER_APP_REPLICA_NAME`, the Container Apps replica, or the hostname when that is unset. `OTEL_RESOURCE_ATTRIBUTES` merges under these; a key set by `Config` wins.

## Environment

| Variable | Read by | Effect |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | nimtel and the SDK | Turns export on. gRPC takes `host:port` or a URL; http/protobuf takes a base URL and appends `/v1/traces`, `/v1/metrics` and `/v1/logs` |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | nimtel | `grpc` (default when unset) or `http/protobuf`; anything else is `ErrProtocol` |
| `OTEL_EXPORTER_OTLP_HEADERS` | SDK | `key=value` pairs sent on every export, such as an ingest key |
| `OTEL_EXPORTER_OTLP_INSECURE` | SDK | Plaintext gRPC; an `http://` endpoint is plaintext already |
| `OTEL_EXPORTER_OTLP_CERTIFICATE`, `OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE`, `OTEL_EXPORTER_OTLP_CLIENT_KEY` | SDK | TLS trust and client certificate |
| `OTEL_EXPORTER_OTLP_COMPRESSION`, `OTEL_EXPORTER_OTLP_TIMEOUT` | SDK | `gzip` and the export timeout in milliseconds |
| `OTEL_EXPORTER_OTLP_{TRACES,METRICS,LOGS}_{ENDPOINT,HEADERS,...}` | SDK | Per signal overrides of the above; the protocol is chosen once, from `OTEL_EXPORTER_OTLP_PROTOCOL` |
| `OTEL_RESOURCE_ATTRIBUTES` | SDK | Extra resource attributes, merged under `Config` |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | nimtel and the SDK | Unset, nimtel samples `ParentBased(TraceIDRatioBased(0.1))`: a tenth of root traces, and every child follows its parent. Set, the SDK sampler from the variables replaces it |
| `OTEL_METRIC_EXPORT_INTERVAL` | nimtel and the SDK | Unset, metrics export every five minutes. Set, the SDK reads it in milliseconds |
| `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE` | nimtel and the SDK | Unset, counters and histograms export as delta, so an idle series sends nothing. Set, `cumulative`, `delta` or `lowmemory` as the exporter reads it |
| `CONTAINER_APP_REPLICA_NAME` | nimtel | `service.instance.id`; set by Container Apps on every replica |

## Runtime and host metrics

The runtime and host feeds add what a node exporter and a process exporter would report, from inside the process and without a sidecar: the Go runtime feed (`go.goroutine.count`, `go.memory.used`, `go.memory.allocated`, `go.memory.gc.cycles`, `go.memory.gc.pause.duration`, `go.cpu.time`) and the host feed (`process.cpu.time`, `system.cpu.time`, `system.memory.usage`, `system.memory.utilization`, `system.network.io`). Both follow the periodic reader interval of five minutes, or `OTEL_METRIC_EXPORT_INTERVAL`. `WithoutMetrics: true` skips both.

## Organization stamp

The samna_auth_middleware stamps every request with the actor's claimius root and owner organization once it has resolved access:

```go
ctx := nimtel.Organization(c.Request().Context(), access.RootID, access.OwnerID)
```

Every log line written with that context, and every span under it, carries `samna.sa_root_id` and `samna.sa_owner_id`.

## How telemetry reaches hvergel and Application Insights

In Azure the service exports to the Container Apps environment's managed OpenTelemetry agent. Once the environment has a telemetry destination, the agent injects `OTEL_EXPORTER_OTLP_ENDPOINT` into every container app and forwards what it receives to each destination. The service holds no endpoint and no secret.

1. hvergel is an OTLP destination. Its `/ingest/v1/*` routes accept http/protobuf, so the destination endpoint is the hvergel base URL ending in `/ingest`:

```
az containerapp env telemetry otlp add \
  --name <environment> --resource-group <group> \
  --otlp-name hvergel --endpoint https://<hvergel host>/ingest \
  --headers "<ingest header>=<key>" \
  --enable-open-telemetry-traces true \
  --enable-open-telemetry-metrics true \
  --enable-open-telemetry-logs true
```

2. Application Insights takes traces and logs; the agent does not send metrics there:

```
az containerapp env telemetry app-insights set \
  --name <environment> --resource-group <group> \
  --connection-string <connection string> \
  --enable-open-telemetry-traces true \
  --enable-open-telemetry-logs true
```

3. Outside the agent, as against a local hvergel, point the service at it directly:

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:<port>/ingest
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
```

## Commands

```
just test
just build
just tidy
```

Releasing is the deploy recipe in the justfile, described in `.claude/skills/deploy/SKILL.md`.

## License

Apache License 2.0. See `LICENSE` and `NOTICE`.
