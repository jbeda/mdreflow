# Changelog

Release notes for mdreflow, newest first.
Each release leads with a short prose summary of what matters — themes, breaking changes, things to act on — followed by grouped details.
The raw commit list is always available via the compare link at the end of each release's notes on GitHub.

Maintenance rule: user-visible changes add a line to the Unreleased section in the same commit.
At release time the section is retitled to the version and the prose lead is written; see RELEASING.md.

## Unreleased

### Fixed

- A width- or sentence-forced split can no longer land a table-delimiter-shaped line (`-:`, `-|-|-`, …) directly under a non-blank line, which made the reflowed paragraph parse as a GFM table (#5, #13); such lines are now backslash-escaped the same way setext underlines already were.
- A paragraph that sits directly under a multi-line link reference definition (no blank line between) is now passed through untouched, since reflowing its first line could invalidate the definition and change what renders (#11). The same protection applies when a titleless one-line definition absorbs its title from the paragraph's first line. Ordinary one-line definitions (`[foo]: /url`, title included or absent-and-unabsorbed) are unaffected — prose after them reflows as before.
- Sentence-mode splits no longer flip between runs when a lone carriage return sits mid-paragraph (#10).
- Escaping a fence-opener-shaped run at a wrapped line start no longer lets its backticks re-pair with unrelated backticks on the next run (which could flip hard-break handling or typography decisions between runs): backticks in that position are now written as `&#96;` — rendering identically — and smart-quote protection is decided against the text as it will actually be emitted (#6, #8, #12).

### Changed

- `Format`, `Check`, and `FormatReader` now reject invalid UTF-8 anywhere in the input with the typed error `mdreflow.ErrInvalidUTF8`; the CLI refuses such files (exit 3) even under `--force` and even when the bad bytes sit past the first 8 KB its quick sniff covers.
- `Format`'s output is now always a fixpoint: formatting a second time returns it unchanged, even on pathological inputs where a single reflow pass is not yet self-consistent (the fuzz-found corners in issues #4–#8, #10–#13). In the never-observed case where reflow will not stabilize at all, the document is returned unchanged rather than churned.

## v0.1.3 (2026-08-08)

Performance fix: width-constrained wrapping no longer blows up on long paragraphs.
Formatting output is unchanged from v0.1.2.

Known caveat: fuzzing has surfaced a family of pre-existing idempotency and render-preservation edge cases (issues [#4–#8, #10–#13](https://github.com/jbeda/mdreflow/issues)), all present since v0.1.0 and hit only on pathological inputs, not normal prose.
They are the next work planned; none affect default sentence mode in normal use.

### Fixed

- Width-constrained modes (`wrap`, and `sentence` with `--max-width`) were roughly cubic in paragraph length: a 1600-word paragraph took 19 seconds to wrap and now takes about 2 milliseconds.
  Output is unchanged (verified byte-identical across the full test corpus and all fuzz seeds).

## v0.1.2 (2026-08-08)

Distribution and documentation; no formatting behavior changed. macOS users can now install with `brew install jbeda/tap/mdreflow`, the README documents modes, configuration, excludes, and the exit-code contract directly instead of deferring everything to `--help`, and `--help` itself got cleaner and more complete.

### Added

- Homebrew: `brew install jbeda/tap/mdreflow` on macOS, `brew install --cask jbeda/tap/mdreflow` on Linux (Homebrew 4.5+ cask support is preliminary there).
  Each release publishes a cask to [jbeda/homebrew-tap](https://github.com/jbeda/homebrew-tap); release tarballs and `go install` remain for everything else.

### Fixed

- `--help` no longer prints a stray one-line usage pointer to stderr above the full help text; stderr is now empty and the full documentation goes to stdout alone.

### Changed

- README now documents modes, configuration, excludes, and the exit-code contract directly (mirroring `--help`, which stays canonical), and the install section points at the release binaries.
- Help text now documents that `--check`/`--diff` work in stdin mode (reporting the input as `-`) and that `--strip-sentence-terminal-breaks` is flag-only with no config-file key.

## v0.1.1 (2026-08-08)

Identical code to v0.1.0, rebuilt with the go1.25.8 toolchain so the shipped binaries clear stdlib vulnerability [GO-2026-4602](https://pkg.go.dev/vuln/GO-2026-4602) (an `os.Root` FileInfo escape, reachable through the directory walk; negligible practical risk for a formatter, but binaries should scan clean).
Use this release instead of v0.1.0.
The module's minimum Go version for library consumers is unchanged.

## v0.1.0 (2026-08-08)

First release. mdreflow reflows Markdown prose — sentence-per-line ([semantic line breaks](https://sembr.org/)) by default, with paragraph-per-line and classic hard wrap on the same pipeline — and touches nothing else: no heading, list, table, or escaping changes, and everything outside reflowed prose passes through byte-for-byte.
It ships as a Go library (`github.com/jbeda/mdreflow`) with a thin CLI, is safe to run unattended (idempotent, render-preserving, loud refusals with a stable exit-code contract), and handles GFM, MDX/Docusaurus, and Hugo content by skipping dialect constructs rather than parsing them.

### Added

- Three modes: `sentence` (default), `para`, and `wrap`, plus `--max-width` secondary clause breaks in sentence mode.
- Dialect skip-list: code blocks, front matter (YAML and TOML), tables, raw HTML, JSX blocks, `:::` directives, math blocks, Hugo shortcodes, GitHub alerts, link reference definitions.
- List and blockquote reflow with correct continuation prefixes (up to two nesting levels; deeper passes through untouched).
- Hard-break preservation with style normalization (`<br>` default, spaces, backslash) and opt-in `--strip-sentence-terminal-breaks`.
- Opt-in typography: `--smart-quotes` and `--ellipses`, applied to prose only — never inside code, links, HTML tags, or skipped blocks.
- CLI: in-place formatting of files and directory trees, `--check`/`--diff` for CI, `.mdreflow.yaml` config with gitignore-syntax excludes, `.gitignore` awareness, non-Markdown refusal, and a fully self-documenting `--help` (exit codes 0 clean / 1 would-reformat / 2 usage or config error / 3 refused).
- pre-commit hooks (`mdreflow`, `mdreflow-check`) and released binaries for linux, macOS, and Windows (amd64/arm64).

### Notes

- Known edge cases are tracked as issues [#1–#8](https://github.com/jbeda/mdreflow/issues); none affect default sentence mode in normal use.
- The library API is v0: `Format`/`Check`/`FormatReader` and `Options` may still change before v1.0.0.
