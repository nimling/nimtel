---
name: deploy
description: Build, test, commit, push, and tag a new version of nimtel so consumers can pick it up via `go get`.
---

# deploy

Ship a new version of `nimtel`. The repo publishes one artefact: the Go module consumed via `go get github.com/nimling/nimtel`. A deploy that skips tests or build verification is a deploy that breaks consumers.

When the user says "deploy" in this repo, execute these steps in order. Stop on the first failure and surface the error.

## 1. Write or update Go tests for meaningful parts of the change

Every piece of new behaviour that has an observable contract needs a test under `test/`. Look at the diff with `git diff` and `git status`, identify the meaningful surface, and either extend `test/init_test.go` or `test/logger_test.go` or add a new test file under `test/`. Trivial moves and rename only diffs do not need a new test. Anything that changes a return value, a sentinel error, a method signature, an enum, or a struct field MUST be covered.

The test must assert the observable contract, not the internal call graph. Hit the public function, assert on the returned value or error sentinel.

## 2. Run the Go tests

```
just test
```

Runs `go test ./test/...`. Failure: report the failing test names and stop. Do not commit, do not bump.

## 3. Build the Go module

```
just build
```

`go build ./...`. Failure: report the build output and stop.

## 4. Commit and push

Stage every changed file relevant to the release. Write a single sentence describing the user visible change. Push to `origin/main`.

```
git add -A
git commit -m "<one sentence describing the change>"
git push
```

If the working tree has nothing to commit, skip the commit but still push so the bump in the next step pushes a clean ref.

## 5. Bump and tag

```
just deploy
```

Calls `sbump patch --env APP_VERSION --push-version --auto --workflow`. Patch bump, tag write, tag push, workflow track. The `--auto` flag means the recipe runs non-interactively for both humans and agents. Consumers of the Go module pick the new tag up via `go get github.com/nimling/nimtel@vX.Y.Z`.

For minor or major releases, invoke the bump explicitly: `sbump major --env APP_VERSION --push-version --auto --workflow` (or `minor`). Do not call `sbump` directly except for non patch bumps. For patch, always go through `just deploy`.

## Never

1. Skip steps 1, 2, or 3. A missing test, a failing test, or a failing Go build must block the deploy.

2. Edit `APP_VERSION` in `.env` by hand. `sbump` owns that field.

3. Use `--no-verify` or `--no-gpg-sign`.

4. Tag manually with `git tag`. The `sbump` script owns version write and tag push together.
