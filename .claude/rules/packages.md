# Package rules

## Boundaries

1. `nimtel` (root): provider setup, the grpc and http/protobuf exporters chosen by `OTEL_EXPORTER_OTLP_PROTOCOL`, the organization stamp, the runtime and host metric feeds, on unless `Config.WithoutMetrics`, the bun query hook `QueryHook` and the http client `Transport`. Depends on stdlib, google/uuid, the OpenTelemetry SDK, exporters, the slog bridge, `go.opentelemetry.io/contrib/instrumentation/runtime`, `go.opentelemetry.io/contrib/instrumentation/host`, `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp`, and `github.com/uptrace/bun` with `bun/extra/bunotel` at the bun version the consumers run. The two feeds emit the semconv `go.*`, `process.cpu.time` and `system.*` instruments and read on the periodic reader interval, five minutes unless `OTEL_METRIC_EXPORT_INTERVAL` is set. Metrics export with delta temporality, and the bun and otelsql instruments are dropped unless `Config.QueryMetrics`.

2. `logger/`: the slog handlers. Depends on stdlib and the OpenTelemetry baggage package only.

3. Nothing app specific enters any package: no models, no schema or query of its own, no samna auth primitives, no imports from consumer repos, no collector endpoint or secret. Transport is the standard OpenTelemetry variables and the platform that sets them.

## Source of truth

1. This module owns telemetry setup for every consumer. A change is made here and released; it is never patched in a consumer's copy, because no such copy is allowed to exist.

2. When a consumer needs new behavior, it is added here behind the existing function surface, released, and then consumed by version bump.

3. The attribute keys `samna.sa_root_id` and `samna.sa_owner_id` are shared with the collector configuration and never change without the collector moving with them.

## Surface

1. `nimtel` (root, main.go): `Config`, `Init`, `QueryHook`, `Transport`, `Organization`, the keys `RootKey`, `OwnerKey`, `EnvironmentKey`, the variables `EndpointEnv`, `ProtocolEnv`, `ReplicaEnv`, `SamplerEnv`, `IntervalEnv`, `TemporalityEnv`, the protocols `ProtocolGRPC`, `ProtocolHTTP`, the defaults `SampleRatio`, `ExportInterval`, `StatementLimit`, the meter scopes `QueryScope`, `StatsScope`, and `ErrServiceName`, `ErrProtocol`.

2. `logger/`: `New` returns a `*slog.Logger` with the JSON handler, or the colour handler in debug, fanned out to any extra handlers, and every baggage member on the context added as an attribute. `Err` is the error attribute.

3. `Config`: `ServiceName` (required, `service.name`), `ServiceVersion` (`service.version`), `Environment` (`deployment.environment.name`), `Debug`, `WithoutMetrics`, `QueryMetrics`. The resource also carries `service.instance.id` from `CONTAINER_APP_REPLICA_NAME`, or the hostname when unset, and merges `OTEL_RESOURCE_ATTRIBUTES` under these keys through `resource.Default()`.

## Contract

1. `Init(ctx, Config)` returns the shutdown function, which calls `ForceFlush` and then `Shutdown` on the trace, metric and log providers with the caller's context.

2. `QueryHook(dbName)` returns the query hook for `db.AddQueryHook`, built on `bunotel`: a client span per query only when the context already carries a valid span, so background loops make no root traces; the span carries `db.name`, `db.operation`, `db.system`, `db.rows_affected` and `db.statement` as the unformatted template cut to `StatementLimit`, 512 bytes, and no `code.*`; errors other than `sql.ErrNoRows` and `sql.ErrTxDone` are recorded with an error status. It still feeds the `go.sql.query_timing` histogram and the `database/sql` pool stats, which `Init` drops through views on `QueryScope` and `StatsScope` unless `Config.QueryMetrics` is true. bunotel is pinned at the bun version the consumers run; moving it moves bun in every consumer.

3. `Transport(base)` returns `otelhttp.NewTransport(base)` for an `http.Client`: a client span per request and trace context injected into the outgoing headers.

4. Two in-process feeds run by default against the meter provider, what a node exporter and a process exporter would give without a sidecar: Go runtime metrics through `go.opentelemetry.io/contrib/instrumentation/runtime` (`go.goroutine.count`, `go.memory.*`, `go.memory.gc.*`, `go.cpu.time`) and host metrics through `go.opentelemetry.io/contrib/instrumentation/host` (`process.cpu.time`, `system.cpu.time`, `system.memory.usage`, `system.memory.utilization`, `system.network.io`). Both are read on the periodic reader interval, `ExportInterval`, five minutes. `WithoutMetrics: true` skips both.

5. `Organization(ctx, rootID, ownerID)` stamps the actor's claimius `sa_root_id` and `sa_owner_id` as baggage and as span attributes under `samna.sa_root_id` and `samna.sa_owner_id`. The collector routes on the root. The samna_auth_middleware calls it once per request from the access struct's `RootID` and `OwnerID`; services never call it themselves. Logs written with a context reach the collector with the trace id and the organization attached.

## Environment

1. `OTEL_EXPORTER_OTLP_ENDPOINT`: turns export on. Unset means the stdout logger only, the local run.

2. `OTEL_EXPORTER_OTLP_PROTOCOL`: `grpc` or unset selects the gRPC exporters, `http/protobuf` the otlp http exporters, anything else is `ErrProtocol`. The http exporters append `/v1/traces`, `/v1/metrics`, `/v1/logs` to the endpoint.

3. `OTEL_EXPORTER_OTLP_HEADERS`, `_INSECURE`, `_CERTIFICATE`, `_CLIENT_CERTIFICATE`, `_CLIENT_KEY`, `_COMPRESSION`, `_TIMEOUT` and the per signal `OTEL_EXPORTER_OTLP_{TRACES,METRICS,LOGS}_*` variants: read by the SDK exporters, never by nimtel.

4. `OTEL_RESOURCE_ATTRIBUTES`: merged into the resource. `CONTAINER_APP_REPLICA_NAME`: `service.instance.id`.

5. `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG`: unset means `ParentBased(TraceIDRatioBased(SampleRatio))`, `0.1`; set, the SDK sampler from the variables wins.

6. `OTEL_METRIC_EXPORT_INTERVAL`: unset means `ExportInterval`, five minutes; set, the SDK reads it in milliseconds. `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE`: unset means delta temporality for counters and histograms; set, the exporter follows it.

## Azure

The Container Apps environment's managed OpenTelemetry agent injects `OTEL_EXPORTER_OTLP_ENDPOINT` into every app once the environment has a destination, and forwards to each one. hvergel is added with `az containerapp env telemetry otlp add` pointing at its `/ingest` base, whose `/ingest/v1/*` routes take http/protobuf; Application Insights with `az containerapp env telemetry app-insights set`, traces and logs only. A session hands these commands to the user, never runs them, `~/.claude/rules/tools.md`.
