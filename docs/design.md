# mdreflow design

*2026-08-07.
Status: M0–M5 implemented — all modes, full skip-list, CLI, typography flags, and release packaging (goreleaser, pre-commit hooks).
This is a living document: design changes land here first, then in code.*

`mdreflow` is a Go library and CLI that reflows Markdown prose.
Its home mode is sentence-per-line ([semantic line breaks](https://sembr.org/)), with paragraph-per-line and hard-wrap modes sharing the same pipeline.
It is a *reflow* tool, not a formatter: it changes where lines break inside paragraph prose and touches nothing else.

- Repo/module/package/binary: `github.com/jbeda/mdreflow` / `mdreflow`
- License: Apache-2.0

## Motivation

Sentence-per-line Markdown makes diffs reviewable (one sentence changed → one
line changed) and gives agents and humans a stable convention for prose edits.
Existing tools have gaps: [flowmark](https://github.com/jlevy/flowmark) misses
sentence boundaries adjacent to inline formatting
([#68](https://github.com/jlevy/flowmark/issues/68)) and bypasses excludes for
explicitly named files ([#43](https://github.com/jlevy/flowmark/issues/43));
[mdslw](https://github.com/razziel89/mdslw) and
[rumdl](https://github.com/rvben/rumdl) are solid but Rust and CLI-only. None
is embeddable as a Go library, which is what other Go tooling needs to format
Markdown it generates or manages.

Agents are a primary driver: they generate and edit a lot of Markdown, they run
the tool unattended, and they occasionally point it at the wrong file. The
design favors loud, machine-legible behavior (exit codes, refusals, complete
`--help`) over interactive polish.

## Goals and non-goals

Goals:

- Reflow paragraph prose in three modes: sentence-per-line, paragraph-per-line, hard wrap.
- Be safe to run unattended: idempotent, render-preserving, loud about anything surprising.
- Work as a Go library first; the CLI is a thin wrapper.
- Handle real-world dialect content (GFM, MDX/Docusaurus, Hugo) by skipping it, not by parsing it.

Non-goals — these are hard principles, and changing them means amending this document first:

- **No normalization.** mdreflow never rewrites block structure: no heading style changes, list marker unification, table alignment, reference-link consolidation, or escaping changes.
  Tools like rumdl do that tier well and compose cleanly with mdreflow because the two touch disjoint constructs.
- **No Markdown renderer.** The Markdown parser is used read-only to locate
  prose; output is produced by splicing reflowed prose into verbatim source
  bytes. This is what keeps the correctness surface small; the multi-year bug
  tails in normalizing formatters (Prettier's proseWrap, mdformat's escaping
  engine) live on the other side of this line.

Explicitly deferred or unlikely (see [Future directions](#future-directions)): formatting Markdown embedded in YAML/JSON values, per-file options in front matter, a `--dialect` selector, content-based file-type sniffing.

## Modes

| Mode | Behavior |
|---|---|
| `sentence` (default) | Join each paragraph's lines, split at sentence boundaries, one sentence per line. With `--max-width N`, sentences longer than N get secondary breaks at clause boundaries (commas, semicolons), falling back to the last word boundary. Without it, long sentences stay long. |
| `para` | Join each paragraph to a single line. |
| `wrap` | Classic hard wrap at `--max-width` (default 80), breaking at word boundaries only. |

All modes are one pipeline — join lines, compute break points, emit — differing only in the break-point strategy.
Continuation lines inside list items and blockquotes are re-indented to match their container.

Width-constrained modes measure candidate lines against the cluster-global no-break spans via a per-cluster prefix table (`widthMeasurer` in `internal/reflow`), built once and queried in O(1); the escape simulation (`escapeBlockInterrupt`) is evaluated exactly only inside the narrow width band where it can change the verdict.
An earlier implementation re-parsed every candidate prefix from scratch, which was roughly cubic (about 30 seconds for a 2000-word paragraph); the rewrite was verified against it by a full-corpus differential run (fixtures plus all fuzz seeds, byte-identical) and the truncated-prefix vs. global-span measurement question is documented on `widthMeasurer`. `BenchmarkFormat` pins the scaling; width-constrained modes must stay near-linear in paragraph size.

## Architecture

```
source bytes
   │
   ├─ goldmark parse (read-only) ──► block map: paragraph byte-ranges,
   │                                 skip-list ranges, inline no-break spans
   │
   ├─ for each paragraph: join lines → find break points → re-emit
   │
   └─ everything else: emitted byte-for-byte
```

[goldmark](https://github.com/yuin/goldmark) (with its GFM extensions enabled
for recognition purposes) parses the document; its AST carries byte offsets
into the source, which is all we need. From the AST we derive:

- **Paragraph ranges**: the blocks eligible for reflow, with their container context (list item, blockquote) for continuation indentation.
- **No-break inline spans** within paragraphs: inline code, links and link destinations, autolinks, images, inline math, footnote references, inline HTML/JSX.
  Break points never land inside these; a span longer than any width limit simply overflows.
- **Skip ranges**: everything below.

### Dialect handling: the skip-list

mdreflow targets one permissive superset of dialects ("do our best on
everything"). Dialect awareness is a *skip-list* — constructs recognized only
well enough to pass through untouched — not a set of dialect implementations.
Each rule is tagged by origin so a future `--dialect` flag can subset the
existing rules rather than require new machinery:

| Construct | Dialects | Handling |
|---|---|---|
| Fenced/indented code blocks | CommonMark | skip block |
| YAML/TOML front matter | GFM tooling, Hugo, Docusaurus | skip block (byte-for-byte) |
| Tables | GFM | skip block |
| Raw HTML blocks | CommonMark | skip block |
| JSX blocks, `{expr}` spans | MDX | skip block / no-break span |
| `:::` directives | Docusaurus/remark | skip fence lines; interior prose reflows |
| Math blocks (`$$`) | GFM, Docusaurus | skip block |
| Hugo shortcodes (`{{< … >}}`, `{{% … %}}`) | Hugo | skip block / no-break span |
| GitHub alert first-lines (`> [!NOTE]`) | GFM | marker line is an immovable boundary (it fuses into the quote's paragraph in the AST — see M0 findings); rest of quote reflows |
| Link reference definitions | CommonMark | skip lines |
| Hard line breaks | CommonMark | immovable boundary (below) |

**Spike M0 outcome** (details and evidence in
[m0-spike-findings.md](m0-spike-findings.md)): goldmark does not parse MDX, and
many dialect constructs do land as `Paragraph` nodes — but a general
line-scanning pre-pass is *not* needed. The block-map derivation step grows
content-pattern rules instead: (1) skip whole `Paragraph` nodes whose text
matches a marker regex (`:::` fences, block shortcodes, block `{expr}`, `+++`
TOML front matter); (2) within a `Paragraph`, treat any `Lines()` segment
matching a marker regex as an immovable boundary — this handles fences fused
with prose by lazy continuation (no-blank-line `:::`, `> [!NOTE]` markers,
paired shortcodes, `$$` math); (3) recognize YAML front matter ourselves via a
byte-range pre-scan (originally goldmark-meta, replaced — see Dependencies);
(4) inline `{expr}`/shortcodes/math are invisible to the AST (plain
`Text`) and need the already-planned regex scan for no-break spans. The one
construct post-parse rules cannot recover is a multi-line JSX opening tag
whose closing `>` sits alone on a line (goldmark consumes it as an empty
blockquote) — documented as a known limitation, revisit with a narrow raw-line
fence if it shows up in real content.

### Sentence segmentation

Regex/punctuation splitting plus an abbreviation exception list — the approach
every shipping tool uses (flowmark, mdslw, rumdl), and the approach whose known
failure modes are documented in their issue trackers, which double as our test
spec. The flowmark #68 lesson is baked in: sentence-terminal punctuation must
be recognized through trailing inline markup (`` `code`. ``, `**bold**.`,
quotes, parens).

Segmentation is behind a public interface so it is independently testable and
swappable (an NLP segmenter, or something smarter, can be plugged in without
touching the pipeline):

```go
// Breaks returns the whitespace gaps separating sentences: for each
// boundary, the [start,end) byte range of the inter-sentence whitespace.
type Segmenter interface {
    Breaks(text string) []Span
}
```

The built-in segmenter ships with a solid default abbreviation list; `Options.Abbreviations` (and the config file) *add* to it.
Wholesale replacement means providing your own `Segmenter`.

### Whitespace and hard breaks

The formatter, not the segmenter, owns whitespace at boundaries:

- At a sentence break, the entire inter-sentence whitespace run is consumed and replaced by the newline. mdreflow never emits trailing whitespace — which matters because trailing double-space is Markdown's hard-break syntax.
- On joins, inter-sentence whitespace normalizes to a single space (required
  for idempotency; also makes two-spaces-after-period typing invisible).
- **Hard line breaks** (trailing double-space, trailing backslash, `<br>`) carry rendered meaning and are immovable: a hard-break line never joins.
  Joining is not "concatenate with spaces."

Hard breaks are preserved but *normalized* to a configurable style:

```go
type HardBreakStyle int
const (
    HardBreakBr        HardBreakStyle = iota // default
    HardBreakSpaces
    HardBreakBackslash
)
```

`<br>` is deliberately the default: it makes the invisible visible.
An accidental double-space hard break survives, but shows up loudly in the diff as `<br>`, prompting the author to remove it or keep it knowingly.
For authors who habitually type two spaces after periods, the opt-in `StripSentenceTerminalBreaks` option treats a trailing double-space *immediately after sentence-terminal punctuation* as accidental and removes it (a documented, flag-reversible exception to render preservation).
Hard breaks anywhere else are always respected.

### Typography (opt-in, off by default)

Span-level substitutions in prose text only (never in code spans or skip
ranges): smart quotes and ellipsis (`...` → `…`). Off by default — Markdown
destined for prompts and tooling wants ASCII — and expressed as bit flags:

```go
type Typography uint
const (
    SmartQuotes Typography = 1 << iota
    Ellipses
)
```

Typography is the documented exception to render preservation (changing quotes
is its purpose).

## Guarantees

Stated as testable promises, enforced by the harness in [Testing](#testing):

1. **Idempotency.** `Format(Format(x)) == Format(x)` for all inputs and option sets.
2. **Render preservation.** Reflow does not change the rendered document,
   verified by comparing goldmark HTML output before and after. Documented
   exceptions: typography flags, hard-break style normalization (renders the
   same `<br>`, different source), and `StripSentenceTerminalBreaks`.
3. **Byte-identical pass-through.** Everything outside reflowed paragraph prose is emitted byte-for-byte.
4. **Check mode.** `--check` and `--diff` report unformatted files without writing, with a stable exit-code contract for CI and agents.

## Library API

Library-first: the root package is the product, `cmd/mdreflow` is a thin
wrapper, internals live in `internal/`. The core is pure bytes-in/bytes-out;
file discovery, config loading, excludes, and input detection are CLI-layer
concerns (promoting them into the library later is non-breaking; the reverse
is not).

```go
package mdreflow // import "github.com/jbeda/mdreflow"

type Mode int
const (
    ModeSentence Mode = iota // default
    ModePara
    ModeWrap
)

type Options struct {
    Mode          Mode
    MaxWidth      int            // 0 = unbounded; on ModeSentence enables secondary clause breaks
    Typography    Typography     // 0 = off
    HardBreaks    HardBreakStyle // default: HardBreakBr
    StripSentenceTerminalBreaks bool
    Abbreviations []string       // additions to the built-in list
    Segmenter     Segmenter      // nil = built-in
}

func Format(src []byte, opts Options) ([]byte, error)
func Check(src []byte, opts Options) (bool, error)

// Convenience wrapper; buffers the full input (Markdown is not streamable —
// e.g. a reference definition on the last line affects the first).
func FormatReader(dst io.Writer, src io.Reader, opts Options) error

func DefaultAbbreviations() []string
```

Every zero value is the default (`Options{}` is valid and sensible): zero `Mode` is sentence mode, zero `MaxWidth` is unbounded, zero `Typography` is off, zero `HardBreakStyle` is `<br>`.
No constructor needed.

`[]byte` (not `string`, not streams) because goldmark's API and AST offsets are byte-based, `os.ReadFile` hands you bytes, and a string API would force copies both directions for no gain.

## CLI

```
mdreflow [flags] [path ...]
```

- **Paths**: files and/or directories; directories are walked recursively for Markdown extensions (`.md`, `.mdx`, `.markdown`).
  Formatting is **in-place** when paths are given — this is a batch tool for pre-commit hooks and agents, where in-place is what you mean.
- **stdin/stdout**: no paths (or `-`) reads stdin and writes stdout.
  This is the pipe mode, and it gives any editor with a "filter through command" binding format-on-demand without an extension.
- **Flags**: `--mode`, `--max-width`, `--check`, `--diff`, `--stdout`, `--force`, `--config`, `--no-gitignore`, plus flags mirroring the typography and hard-break options.
  Stdlib `flag`; the surface is one command.
- **Exit codes** (a contract, since agents branch on them): `0` success/clean; `1` would-reformat (`--check`/`--diff` only); `2` usage or config error; `3` refused input (not Markdown, or excluded without `--force`).
- **`--help` is a first-class deliverable**: complete flag docs, the exit-code contract, and examples in the help text itself, so an unattended agent can self-serve without a README.

### Configuration

`.mdreflow.yaml`, discovered upward from each target file.
Defaults should be good enough that most repos need no file.
Precedence: flags > config file > built-in defaults.

```yaml
mode: sentence        # sentence | para | wrap
max-width: 0
typography: []        # e.g. [smart-quotes, ellipses]
hard-breaks: br       # br | spaces | backslash
abbreviations:        # additions to the built-in list
  - "et al."
exclude:              # gitignore syntax
  - "CHANGELOG.md"
  - "generated/**"
```

### Excludes

- The repo's `.gitignore` is respected by default (`--no-gitignore` disables) — node_modules and build output are almost always already covered.
- The config `exclude:` list (gitignore syntax) covers tracked-but-generated files.
- Built-in always-excludes underneath: `.git/`, `node_modules/`, `vendor/`.
- **Excludes apply even to explicitly named files** — the flowmark #43 failure, designed out.
  A skipped explicit path prints `skipped (excluded by …)`; `--force` overrides.

Candidate libraries (implementation-time decision; the behavior is the
commitment, the library is a detail):
[boyter/gocodewalker](https://github.com/boyter/gocodewalker) for tree walking
that honors nested `.gitignore`, and
[sabhiram/go-gitignore](https://github.com/sabhiram/go-gitignore) for matching
config patterns. Caveat: no Go gitignore implementation is fully spec-compliant
(see [go-git#108](https://github.com/go-git/go-git/issues/108)), so the test
suite compares our matching against `git check-ignore` on a synthetic tree.

### Non-Markdown input detection

The observed failure mode is an unattended agent running the formatter on a YAML or JSON file.
V1 defense, deliberately cheap:

- **Extension check** on explicitly named files: known-Markdown extensions pass; known-other extensions (`.yaml`, `.json`, `.go`, …) are refused.
- **Binary sniff**: NUL bytes or invalid UTF-8 in the first 8&nbsp;KB → refused.

Refusal is exit code `3` with a one-line explanation and no write; `--force`
overrides. Refusing (vs. warning) is the point: a warning in scrolled-past
output doesn't stop an unattended agent, a non-zero exit does. Directory walks
are unaffected (they only pick up Markdown extensions). Content-based sniffing
for unknown extensions ("parses as YAML/JSON with no Markdown block
constructs") is deferred until mislabeled files are observed in practice — the
heuristic's second half is fuzzy, and the extension check covers the failures
actually seen.

## Testing

The wrapping logic is heuristic; a deep test corpus is the only durable defense against regressions, and the guarantees above are only as real as this harness.

1. **Golden-file fixtures** (`testdata/`): input → expected output pairs covering every skip-list construct, every mode, every option.
   Fixture content is synthetic (lorem-ipsum-style or purpose-written), reproducing *structures* observed in real repos without lifting content.
2. **Property tests over the corpus**: idempotency and render preservation (goldmark HTML before == after, documented exceptions aside) run automatically over every fixture — adding a fixture adds the property checks for free.
3. **Mined regression cases**: bugs from flowmark's, mdslw's, and rumdl's issue trackers re-authored (not copied) as fixtures — e.g. flowmark #68's `` `token`. `` boundary misses.
4. **Segmenter suite**: the sentence splitter tested standalone against a
   Golden-Rules-style case list (in the style of
   [pragmatic_segmenter](https://github.com/diasks2/pragmatic_segmenter)'s
   golden rules, cases our own).
5. **Exclude parity**: our gitignore matching vs. `git check-ignore` output on a synthetic tree.
6. **Fuzzing**: Go native fuzzing on `Format` with crash, idempotency, and render preservation as oracles.

## Dependencies

Small on purpose; additions require amending this doc.

- `github.com/yuin/goldmark` (+ GFM extension) — parsing, library core
- `github.com/goccy/go-yaml` — config file, CLI layer only. (Amended
  2026-08-07: originally `gopkg.in/yaml.v3`, which is no longer maintained;
  goccy/go-yaml is actively maintained with better spec compliance.)
- YAML front matter is recognized by our own byte-range pre-scan
  (`internal/blockmap`), not a parser extension. (Amended 2026-08-07: this was
  goldmark-meta at first, but mdreflow never consumes the parsed metadata —
  it only needs the block excluded from reflow — and goldmark-meta's YAML
  parsing dragged in unmaintained `yaml.v2` plus parser-hook artifacts the
  fuzz harness had to defend against. Opener is exactly `---` at byte 0;
  unterminated front matter is not front matter.)
- `github.com/boyter/gocodewalker` — CLI layer only; used solely for its
  vendored `go-gitignore` subpackage (nested-`.gitignore` semantics). The walk
  itself is hand-rolled `filepath.WalkDir` (M4 decision; sabhiram/go-gitignore
  was evaluated and dropped — no nested-file support).
- `github.com/pmezard/go-difflib` — CLI layer only, unified diffs for `--diff` (M4 addition; hand-rolling Myers diff wasn't worth the risk).
- Stdlib `flag` for the CLI (single command; cobra buys weight, not function;
  the cost is shell completions, acceptable for now)

## Milestones

Vertical slice first — a thin end-to-end path that is immediately usable and carries the test harness from day one — then broaden.

- **M0 — spike**: goldmark vs. MDX/Docusaurus/Hugo input.
  Where do JSX blocks, `{}` expressions, `:::` directives, and shortcodes land in the AST?
  Decides whether a fence-scanning pre-pass is needed.
  This is the load-bearing unknown; it runs before any pipeline code hardens.
- **M1 — vertical slice**: `mdreflow < in.md > out.md`.
  Sentence mode only; pipeline (parse → paragraph map → join → segment → emit); minimal skip-list (code fences, front matter); built-in segmenter with default abbreviations; golden harness with idempotency + render-preservation properties.
  Usable via pipes and editor filters from this point on.
- **M2 — correctness breadth**: full skip-list, no-break inline spans, hard-break styles and whitespace rules, list/blockquote continuation indentation, mined regression fixtures, fuzzing.
- **M3 — modes**: `para`, `wrap`, `--max-width` secondary clause breaks.
- **M4 — CLI proper**: path/directory handling, in-place writes, config discovery, excludes + gitignore, `--check`/`--diff`, input detection, exit codes, agent-grade `--help`.
- **M5 — ship**: typography flags, goreleaser binaries, `.pre-commit-hooks.yaml`, README and docs polish.

Versioning: v0.x until the library API has survived real use; v1.0.0 is an API-stability promise (Go module semantics make it binding).
CI: GitHub Actions — test, vet, golangci-lint on push; goreleaser on tags.

## Future directions

Recorded so the design doesn't preclude them; none are commitments.

- **Markdown embedded in YAML/JSON values** — the original wishlist item, now judged a large scope expansion of uncertain value.
  The hard part is knowing *which* fields are Markdown; the shape would be caller-supplied yq/jq-style path selectors plus comment-preserving YAML node surgery, with mdreflow formatting the extracted strings.
  The bytes-in/bytes-out library API is the only accommodation made now: it keeps this buildable from outside.
- **Per-file options in front matter** — a reserved key (e.g.
  `mdreflow: {mode: para}`) slotting into the precedence chain as flags >
  front matter > config > defaults.
- **`--dialect` flag** — subsets of the tagged skip-list rules.
- **Content sniffing for unknown extensions** — gated on observed need.
- **GitHub Action, Homebrew tap, editor extensions** — distribution beyond goreleaser + pre-commit.
- **Alternative segmenters** — NLP (e.g. jdkato/prose) or model-based splitting behind the `Segmenter` interface, if the regex approach's ceiling is ever actually hit.
