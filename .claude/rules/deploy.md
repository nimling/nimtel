# Deploy rules

This repo publishes one artefact: the Go module consumed via `go get github.com/nimling/nimtel`. The full procedure lives at `.claude/skills/deploy/SKILL.md`. The constraints below apply outside of any single deploy run.

## Never

1. Edit `APP_VERSION` in `.env` by hand. `sbump` owns that field. Hand edits drift from the tag and corrupt the bump sequence.

2. Tag manually with `git tag` or `git push --tags`. `just deploy` writes the tag and pushes it as one operation via `sbump`.

3. Use `--no-verify` or `--no-gpg-sign` on commits going out as part of a release. Hooks and signing are part of the contract.

4. Skip `just test` or `just build` before `just deploy`. A red build or a failing test must block the bump.

5. Commit generated or unrelated files together with a release. Stage only the source changes and the test changes that describe the release.

## Always

1. Update or add Go tests under `test/` for any change that has an observable contract.

2. Write a single sentence commit message that describes the user visible change. The release notes derive from `git log` after the tag.

3. Use the existing `justfile` recipes: `just test`, `just build`, `just deploy`. Direct `go test`, `go build`, or `sbump` invocation drifts from the contract.

## Version policy

`just deploy` does a patch bump. For minor or major, run `sbump minor|major --env APP_VERSION --push-version --auto --workflow` on the user's word. The repo does not gate this; the human running the deploy picks the bump.
