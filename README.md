# mdreflow

[![CI build status badge](https://github.com/jbeda/mdreflow/actions/workflows/ci.yaml/badge.svg)](https://github.com/jbeda/mdreflow/actions/workflows/ci.yaml)
[![Go reference documentation badge](https://pkg.go.dev/badge/github.com/jbeda/mdreflow.svg)](https://pkg.go.dev/github.com/jbeda/mdreflow)
[![Latest release version badge](https://img.shields.io/github/v/release/jbeda/mdreflow)](https://github.com/jbeda/mdreflow/releases/latest)

Reflow Markdown prose.
The default mode is sentence-per-line ([semantic line breaks](https://sembr.org/)); paragraph-per-line and classic hard wrap share the same pipeline.
It is a Go library first, with a thin CLI on top.

mdreflow changes where lines break inside paragraph prose and touches nothing else.
It never rewrites block structure (headings, list markers, tables, escaping) and produces output by splicing reflowed prose into the original bytes, so everything outside a reflowed paragraph passes through byte-for-byte.
If you also want lint-style normalization, pair it with a tool like [rumdl](https://github.com/rvben/rumdl); the two touch disjoint parts of the file.

Why sentence-per-line?
Diffs.
One changed sentence is one changed line, which makes prose reviews readable and gives both humans and agents a stable convention for edits.

**Status: early v0.** The library API and CLI surface are still moving; every milestone in the design doc is implemented, but nothing is frozen until v1.0.0.

## Install

Via [Homebrew](https://brew.sh/):

```
brew install jbeda/tap/mdreflow           # macOS
brew install --cask jbeda/tap/mdreflow    # Linux (Homebrew 4.5+, preliminary cask support)
```

Prebuilt binaries for Linux, macOS, and Windows (amd64 and arm64) are on the [releases page](https://github.com/jbeda/mdreflow/releases), with a `checksums.txt` alongside.
Unpack the archive and put `mdreflow` on your `PATH`.

Or build from source:

```
go install github.com/jbeda/mdreflow/cmd/mdreflow@latest
```

## Usage

`mdreflow --help` is the complete reference and stays canonical: every flag, the exit-code contract, the config format, and worked examples.
The sections below cover the parts you will look up most.

```
mdreflow docs/                  # format a tree in place (respects .gitignore + excludes)
mdreflow --check docs/          # CI gate: exit 1 if anything would change
mdreflow --diff README.md       # show what would change
mdreflow < in.md > out.md       # pipe mode, e.g. an editor filter binding
```

With path arguments, mdreflow formats in place: files directly, directories by walking them for `.md`, `.mdx`, and `.markdown` files.
With no arguments (or `-`), it reads stdin and writes stdout; `--check` and `--diff` work there too, reporting the input as `-`.
Never redirect a file onto itself in pipe mode (the shell truncates it first); use in-place mode for that.

As a library:

```go
import "github.com/jbeda/mdreflow"

out, err := mdreflow.Format(src, mdreflow.Options{})
```

`Options{}` is valid and sensible: sentence mode, no width limit.
The full API reference is on [pkg.go.dev](https://pkg.go.dev/github.com/jbeda/mdreflow).

## Modes

- `sentence` (default): one sentence per source line. `--max-width` optionally adds clause-level breaks inside sentences that run past the limit; the default of 0 means unbounded. Non-zero widths below 20 are refused in every mode — very narrow wrapping forces breaks inside Markdown constructs.
- `para`: each paragraph joined onto a single line. `--max-width` is an error here.
- `wrap`: classic hard wrap at `--max-width` (default 80).

## Dialects

The default dialect, `gfm`, is the permissive GitHub-flavored superset: GFM extensions plus footnotes. Docusaurus, Hugo, and MDX constructs are recognized and passed through untouched in every dialect.
`--dialect mkdocs` additionally reflows MkDocs/Python-Markdown admonition bodies (`!!! note` followed by a 4-space-indented body), which CommonMark parsers can only see as indented code blocks — opt-in because reflowing one changes what a CommonMark renderer emits.
Recognition is deliberately narrow: bodies containing a fence marker or more than one paragraph are left alone.

## Configuration

mdreflow looks for `.mdreflow.yaml` by walking upward from each target file (from the current directory in pipe mode), or reads the file given with `--config`.
Precedence is flags > config file > built-in defaults, and a flag explicitly given on the command line wins even if its value equals the default.
Unknown keys and unrecognized values are a loud error (exit 2), not a silent no-op, so a typo cannot quietly change behavior.

```yaml
mode: sentence          # sentence | para | wrap
dialect: gfm            # gfm | mkdocs (mkdocs also reflows admonition bodies)
max-width: 0
hard-breaks: br         # br | spaces | backslash
abbreviations:          # additions to the built-in list
  - "et al."
exclude:                # gitignore syntax, matched like a .gitignore
  - "CHANGELOG.md"
  - "generated/**"
```

These are all the keys; `--strip-sentence-terminal-breaks` is flag-only.

## Excludes

Excludes are checked in order, first match wins: the built-in always-excludes (`.git/`, `node_modules/`, `vendor/`), then the repository's `.gitignore` files (nested ones included; `--no-gitignore` disables), then the config file's `exclude:` patterns.
A directory walk skips excluded files silently.
A file named explicitly on the command line is refused loudly instead (exit 3), so an agent told to format a generated file finds out rather than silently succeeding; `--force` overrides.

## Exit codes

- `0`: nothing needed to change.
- `1`: `--check` or `--diff` found at least one file that would be reformatted; nothing was written.
- `2`: usage or config error; the run aborts immediately.
- `3`: at least one input was refused (excluded, or not recognized as Markdown) without `--force`; the rest of the batch still runs.

When several apply in one run, the most severe wins: 2 > 3 > 1 > 0.
The numbers follow Unix convention (diff-style 1, usage-error 2), so severity does not track numeric order.

## pre-commit

```yaml
repos:
  - repo: https://github.com/jbeda/mdreflow
    rev: v0.1.5 # or any commit
    hooks:
      - id: mdreflow # formats staged Markdown in place
      # - id: mdreflow-check  # or: fail the commit instead of rewriting
```

Both hooks cover `.md`, `.mdx`, and `.markdown`.
The `mdreflow` hook rewrites files and lets pre-commit re-stage them.
The `mdreflow-check` hook writes nothing and fails if anything would change, which is the shape you want in CI.

## Documentation

- [pkg.go.dev/github.com/jbeda/mdreflow](https://pkg.go.dev/github.com/jbeda/mdreflow) is the library API reference, rendered from the doc comments.
- [docs/design.md](docs/design.md) is the canonical design: goals, modes, architecture, dialect handling (GFM, MDX/Docusaurus, Hugo), guarantees, API, CLI, and milestones.
  Design changes land there before code.
- [docs/m0-spike-findings.md](docs/m0-spike-findings.md) maps how dialect constructs land in goldmark's AST and why the skip-list works the way it does.

## License

Apache-2.0.
