# mdreflow

Reflow Markdown prose.
The default mode is sentence-per-line ([semantic line breaks](https://sembr.org/)); paragraph-per-line and classic hard wrap share the same pipeline.
It is a Go library first, with a thin CLI on top.

mdreflow changes where lines break inside paragraph prose and touches nothing
else. It never rewrites block structure (headings, list markers, tables,
escaping) and produces output by splicing reflowed prose into the original
bytes, so everything outside a reflowed paragraph passes through
byte-for-byte. If you also want lint-style normalization, pair it with a tool
like [rumdl](https://github.com/rvben/rumdl); the two touch disjoint parts of
the file.

Why sentence-per-line?
Diffs.
One changed sentence is one changed line, which makes prose reviews readable and gives both humans and agents a stable convention for edits.

**Status: early v0.** The library API and CLI surface are still moving; every milestone in the design doc is implemented, but nothing is frozen until v1.0.0.

## Install

```
go install github.com/jbeda/mdreflow/cmd/mdreflow@latest
```

Release archives for linux, macOS, and Windows (amd64 and arm64) are built by [goreleaser](.goreleaser.yaml) on each `v*` tag.
No tag has been cut yet, so there is nothing on the releases page today; `go install` is the way in for now.

## Usage

`mdreflow --help` is the complete reference: every flag, the exit-code contract (0 clean, 1 would-reformat, 2 usage/config error, 3 refused input), the `.mdreflow.yaml` config format, and worked examples.
The short version:

```
mdreflow docs/                  # format a tree in place (respects .gitignore + excludes)
mdreflow --check docs/          # CI gate: exit 1 if anything would change
mdreflow --diff README.md       # show what would change
mdreflow < in.md > out.md       # pipe mode, e.g. an editor filter binding
```

As a library:

```go
import "github.com/jbeda/mdreflow"

out, err := mdreflow.Format(src, mdreflow.Options{})
```

`Options{}` is valid and sensible: sentence mode, no width limit, typography off.

## Typography

`--smart-quotes` curls straight quotes and `--ellipses` turns `...` into `…`.
Both are off by default, because Markdown headed for prompts, diffs, and tooling usually wants plain ASCII, and both are the only options that change how a document renders.
They apply to paragraph prose only: never inside inline code, links, autolinks, math, shortcodes, `{expr}` spans, or inline HTML, and never inside a skipped block like a code fence, front matter, or a table.
Set them in `.mdreflow.yaml` as `typography: [smart-quotes, ellipses]`.

## pre-commit

```yaml
repos:
  - repo: https://github.com/jbeda/mdreflow
    rev: v0.1.0 # or any commit
    hooks:
      - id: mdreflow # formats staged Markdown in place
      # - id: mdreflow-check  # or: fail the commit instead of rewriting
```

Both hooks cover `.md`, `.mdx`, and `.markdown`.
The `mdreflow` hook rewrites files and lets pre-commit re-stage them.
The `mdreflow-check` hook writes nothing and fails if anything would change, which is the shape you want in CI.

## Documentation

- [docs/design.md](docs/design.md) is the canonical design: goals, modes, architecture, dialect handling (GFM, MDX/Docusaurus, Hugo), guarantees, API, CLI, and milestones.
  Design changes land there before code.
- [docs/m0-spike-findings.md](docs/m0-spike-findings.md) maps how dialect constructs land in goldmark's AST and why the skip-list works the way it does.

## License

Apache-2.0.
