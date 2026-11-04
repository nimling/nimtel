# Changelog

## v1.0.0

The first public release.

1. `Init` installs traces, metrics and logs against an OTLP collector named by the standard `OTEL_EXPORTER_OTLP_*` variables, over gRPC or http/protobuf, and a `slog` default that also writes to stdout.
2. The resource carries the service name, version, environment and instance.
3. Traces are sampled at a tenth of root traces with children following their parent, and metrics export as delta every five minutes, unless the standard variables say otherwise.
4. Runtime and host metrics are reported from every process by default.
5. `QueryHook` traces bun queries, and `Transport` traces outgoing http requests and carries the trace to the next service.
6. `Organization` stamps the actor's root and owner organization on every log line and span under a context.
