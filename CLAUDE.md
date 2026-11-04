# nimtel

The shared telemetry module for the Nimtech Go servers, consumed with `go get github.com/nimling/nimtel`. One `Init` per service installs the OpenTelemetry trace, metric and log providers, the OTLP exporters read from the standard `OTEL_EXPORTER_OTLP_*` variables, and a `slog` default that writes to stdout and to the collector at once; `logger/` holds the slog handlers. Transport is the platform: the Container Apps environment agent injects the endpoint, nothing here knows where the collector is. The surface, the contract of every exported function and the variables read are `.claude/rules/packages.md`.

## Commands

1. `just install`: the `sbump` binary.
2. `just build`: `go build ./...`. `just tidy`: `go mod tidy`.
3. `just test [target]`: `go test ./test/...`, or one folder under `test/`.
4. `just deploy`: sbump patch bump on `APP_VERSION`, tag and push, following the `Create Release` workflow.

## Conventions

1. Nothing app specific enters this module: no models, no samna auth, no consumer imports, no endpoints or secrets. The allowed dependencies are listed in `.claude/rules/packages.md`.
2. The attribute keys `samna.sa_root_id` and `samna.sa_owner_id` are frozen contracts shared with the collector configuration, named after the claimius columns they carry.
3. The echo server middleware is `smw.TelemetryMiddleware()` in samna_auth_middleware; it is never duplicated here, and services never call `Organization` themselves.
4. Every change with an observable contract gets a test under `test/` before it ships.
5. Release work goes through the recipes, never a raw `go` command or a hand `git tag`; `~/.claude/rules/tools.md`.
6. Code, files, git and stack rules are `~/.claude/rules/` and are not restated here.

## Rules

1. `.claude/rules/packages.md`: every session, the package boundaries, the contract of every exported function and the environment variables read.
2. `.claude/rules/deploy.md`: every session, the release guardrails.

## Skills

1. `.claude/skills/deploy`: test, build, commit, push, then the bump; consumers upgrade with `go get github.com/nimling/nimtel@<tag>`.
2. User level, invoked by name: `git`, `github`, `review`.
