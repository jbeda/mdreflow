# Releasing mdreflow

Mechanical steps live in `Taskfile.yaml` (`task --list`); this doc is the procedure that strings them together.
Release notes are narrative-first: CHANGELOG.md is the source of truth, and the release workflow extracts the tagged version's section into the GitHub release automatically (`scripts/release-notes.sh`).

## During development

Every user-visible change adds a line to CHANGELOG.md's `## Unreleased`
section **in the same commit**.
Write entries in user terms ("`--smart-quotes` no longer curls quotes inside
HTML attributes"), not commit-message terms.

## Cutting a release

One-time prerequisite: the Release workflow publishes a Homebrew cask to
[jbeda/homebrew-tap](https://github.com/jbeda/homebrew-tap) and needs a
`HOMEBREW_TAP_GITHUB_TOKEN` repo secret (fine-grained PAT, contents
read/write on that repo alone).

1. Retitle `## Unreleased` to `## vX.Y.Z (YYYY-MM-DD)` (local date) and write
   the prose lead: the release's theme, anything users must act on (breaking
   changes, new defaults, upgrade notes), and the one or two things worth
   caring about.
   Start a fresh empty `## Unreleased` above it.
2. `task release-prep VERSION=vX.Y.Z` — bumps version references in the README (currently the pre-commit `rev:` example) to the new tag.
3. `task verify` and, if reflow logic changed since the last release, `task fuzz`.
4. Sanity-check the notes render: `task release-notes VERSION=vX.Y.Z` (the
   compare link appears once the tag exists; a warning before that is fine).
5. Commit, push, then tag and push the tag: `git tag -a vX.Y.Z -m "mdreflow vX.Y.Z" && git push origin vX.Y.Z`.
   The Release workflow refuses tags whose commit is not on `main` (push the branch first, then the tag).
6. Watch both tag-triggered workflows (Release and govulncheck): `gh run list -R jbeda/mdreflow --branch vX.Y.Z`.
   - If govulncheck fails on a **stdlib** vulnerability, the fix is bumping the `toolchain` directive in go.mod (not deps) and cutting a patch release; this happened for v0.1.1 / GO-2026-4602.
7. `task release-verify VERSION=vX.Y.Z` — downloads a real asset, verifies the checksum, smoke-tests the binary, then pulls the version through proxy.golang.org (so `go install @latest` sees it) and asks pkg.go.dev to index it.

## Versioning

v0.x until the library API has survived real use; v1.0.0 is an API-stability promise (design.md's versioning note).
Go modules require full semver tags (`v0.2.0`, not `v0.2`).
