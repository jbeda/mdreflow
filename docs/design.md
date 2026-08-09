# mdreflow design

*2026-08-07.
Status: M0–M5 implemented — all modes, full skip-list, CLI, and release packaging (goreleaser, pre-commit hooks); typography shipped in M5 and was later removed (see Typography).
This is a living document: design changes land here first, then in code.*

`mdreflow` is a Go library and CLI that reflows Markdown prose.
Its home mode is sentence-per-line ([semantic line breaks](https://sembr.org/)), with paragraph-per-line and hard-wrap modes sharing the same pipeline.
It is a *reflow* tool, not a formatter: it changes where lines break inside paragraph prose and touches nothing else.

- Repo/module/package/binary: `github.com/jbeda/mdreflow` / `mdreflow`
- License: Apache-2.0

## Motivation

Sentence-per-line Markdown makes diffs reviewable (one sentence changed → one line changed) and gives agents and humans a stable convention for prose edits.
Existing tools have gaps: [flowmark](https://github.com/jlevy/flowmark) misses sentence boundaries adjacent to inline formatting ([#68](https://github.com/jlevy/flowmark/issues/68)) and bypasses excludes for explicitly named files ([#43](https://github.com/jlevy/flowmark/issues/43)); [mdslw](https://github.com/razziel89/mdslw) and [rumdl](https://github.com/rvben/rumdl) are solid but Rust and CLI-only.
None is embeddable as a Go library, which is what other Go tooling needs to format Markdown it generates or manages.

Agents are a primary driver: they generate and edit a lot of Markdown, they run the tool unattended, and they occasionally point it at the wrong file.
The design favors loud, machine-legible behavior (exit codes, refusals, complete `--help`) over interactive polish.

## Goals and non-goals

Goals:

- Reflow paragraph prose in three modes: sentence-per-line, paragraph-per-line, hard wrap.
- Be safe to run unattended: idempotent, render-preserving, loud about anything surprising.
- Work as a Go library first; the CLI is a thin wrapper.
- Handle real-world dialect content (GFM, MDX/Docusaurus, Hugo) by skipping it, not by parsing it.

Non-goals — these are hard principles, and changing them means amending this document first:

- **No normalization.** mdreflow never rewrites block structure: no heading style changes, list marker unification, table alignment, reference-link consolidation, or escaping changes.
  Tools like rumdl do that tier well and compose cleanly with mdreflow because the two touch disjoint constructs.
- **No Markdown renderer.** The Markdown parser is used read-only to locate prose; output is produced by splicing reflowed prose into verbatim source bytes.
  This is what keeps the correctness surface small; the multi-year bug tails in normalizing formatters (Prettier's proseWrap, mdformat's escaping engine) live on the other side of this line.

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

[goldmark](https://github.com/yuin/goldmark) (with its GFM extensions enabled for recognition purposes) parses the document; its AST carries byte offsets into the source, which is all we need.
From the AST we derive:

- **Paragraph ranges**: the blocks eligible for reflow, with their container context (list item, blockquote) for continuation indentation.
- **No-break inline spans** within paragraphs: inline code, links and link destinations, autolinks, images, inline math, footnote references, inline HTML/JSX.
  Break points never land inside these; a span longer than any width limit simply overflows.
- **Skip ranges**: everything below.

### Convergence: reflow runs to fixpoint

A single pipeline pass plans breaks from the *pre*-reflow parse, but the guarantees are judged on the *post*-reflow reparse.
Fuzzing showed a persistent family of corners where the two disagree: the emitted text parses slightly differently than the planner assumed (an escape changes code-span pairing, a consumed double space changes a sentence-boundary verdict, a join changes a link-reference-definition skip decision), so a second run lands differently than the first.

The goal is single-pass convergence: the planner should predict its own output's reparse correctly the first time, and every known divergence gets a root-cause fix.
But unknown shapes will keep existing, so `Format` also makes idempotency structural as a backstop: it runs the pipeline, then re-runs it on its own output until the output is stable, up to a small cap (4 passes; in practice the first re-run already matches).
If the cap is hit without convergence — including a cycle where two outputs alternate — `Format` returns the **original document unchanged**.
Document-level, not per-paragraph: the known divergence modes merge or re-split paragraphs across passes, so "the same paragraph, one pass later" is not a stable identity to fall back on.
"We could not safely flow this" is expressed as a no-op, never as churn.

The backstop is for users; internally it is treated as a bug detector.
The test and fuzz harnesses disable it (test-only switch) and drive the single-pass core, so any input that needs the backstop shows up as a failing idempotency oracle to be root-caused — never silently absorbed.

### Render preservation is also structural: the render backstop

Idempotency's twin guarantee gets the same treatment.
(Amendment 2026-08-09; supersedes the earlier stance that render hazards were planner obligations *only*, with the fuzz oracle as sole referee.)
After the fixpoint loop settles, `Format` renders the input and the candidate output through the same goldmark configuration the parse used (see Dialects) and compares normalized HTML — the same normalization the fuzz render oracle applies, promoted from test code to an internal package.
On any difference, `Format` returns the **original document unchanged** — document-level, for the same reasons as the idempotency fallback, and expressed the same way: "we could not safely flow this" is a no-op, never corruption.

This inverts the safety architecture.
The blockmap guards (spanning-delimiter checks, line-start hazard filtering, definition zones) stop being the mechanism that makes render preservation true and become a coverage mechanism: their job is maximizing how much prose reflows without tripping the backstop, and an incomplete guard now costs a missed reflow instead of corrupted content.
Guard narrowing becomes correspondingly lower-stakes.

One carve-out, for dialect recognitions that *reinterpret* a construct: under the mkdocs dialect an admonition body is prose to the target renderer but an indented code block to goldmark, so reflowing it changes goldmark's render by design, and a naive comparison would veto every such reflow.
The backstop therefore checks each dialect-recognized reflow-eligible range against its own invariant — the word sequence is unchanged, only inter-word whitespace moved (for an indented block, goldmark's `<pre>` output preserves line breaks, so whitespace-insensitive comparison of that block's rendered text is exactly this check) — and the whole-document comparison normalizes those ranges' rendered content identically on both sides.
Everything outside a recognized range keeps the exact comparison.
This is not a loosening in disguise: "words unchanged, whitespace moved" is the same promise reflow makes for an ordinary paragraph; it is simply enforced directly where goldmark's semantics cannot express it.

What the backstop measures is self-consistency under goldmark's semantics: input and output pass through the same parser, so goldmark's own bugs largely cancel — the comparison still detects whether reflow changed anything *under those semantics*.
What it cannot measure is goldmark-vs-target-renderer divergence (Python-Markdown above all; cmark-gfm marginally; Hugo not at all, Hugo *is* goldmark).
That exposure predates the backstop — the planner has always used goldmark's AST as its parse authority — and is bounded by conservative per-dialect recognition and external verification (site-build diffs), not by this check.

Same discipline as the idempotency backstop: it is for users, internally a bug detector.
The harnesses disable it and assert render preservation as a failing oracle instead, so every shape that would trip it gets root-caused.
A *false* trip — the backstop suppressing a legitimate reflow of ordinary prose, most plausibly through an over-strict normalization — is itself a bug class, watched by running the corpus with the backstop enabled and asserting zero fallbacks.

Two things the backstops deliberately do *not* do:

- They do not excuse planner bugs.
  Every hazard remains a planner obligation fixed at the root, with the fuzz oracles as referee; the backstops only bound the blast radius of the shapes nobody has found yet.
- They are not a license for sloppy single-pass behavior.
  Each known divergence shape gets a root-cause fix; the backstops turn unknown shapes from correctness bugs into (at worst) unreflowed documents.

Cost: the common case is exactly two pipeline passes (one to reflow, one to confirm stability) plus two parse-and-renders for the comparison.
Post the width-measurement fix a pass is milliseconds even on large documents; the benchmark suite pins this and grows a render-backstop benchmark alongside.

### Dialects: renderer profiles, and the skip-list

(Amendment 2026-08-09, with PR #19 as the first consumer.)
`Options.Dialect` — a single-select enum mirroring `Mode`'s shape, with `--dialect` and a `dialect:` config key parsed loudly like `mode` — names the renderer profile a tree targets.
A dialect is a bundle, not a feature flag.
It selects:

1. **The goldmark configuration** used for parsing *and* for the render backstop's comparison, so "render-preserving" always means "under the target's semantics."
2. **Which flavour-specific block recognitions are on.** Internally each recognition is a plain feature bit (`reflow.Options.MkDocs` is the first); dialects bundle bits at the `Format` boundary, and the bits themselves are never user-visible.
   If finer-grained selection is ever needed, the bits are already there — dialects would become named bundles over them — but single-select is the bet that we won't need it.
3. Eventually, which of the skip-list rules below even apply — a construct the profile's parser cannot produce stops being a hazard *automatically* under the render backstop: with tables off, a manufactured delimiter-shaped line is prose on both sides of the comparison.
   Over-cautious guards therefore cost coverage, never correctness, and can be narrowed per-dialect lazily or not at all.

The zero value, `DialectGFM` (`--dialect gfm`), is exactly the permissive GFM-plus-footnotes configuration the parser has always hardcoded — existing behavior, renamed rather than changed.
`commonmark` is deliberately *reserved* for a future strict CommonMark profile (GFM extensions off) rather than aliased to the default: aliasing it now would make the name unavailable for the one profile it accurately describes.
`mkdocs` layers admonition-body recognition on the GFM base; its true target renderer (Python-Markdown) is not CommonMark and cannot be modeled by our oracle, so its recognitions must stay narrow and are verified externally (full `mkdocs build` diffs), per the render-backstop section's divergence caveat.

For everything else mdreflow still targets one permissive superset of dialects ("do our best on everything").
Dialect awareness beyond the profile is a *skip-list* — constructs recognized only well enough to pass through untouched — not a set of dialect implementations.
Each rule is tagged by origin so dialect profiles can subset the existing rules rather than require new machinery:

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
| Link reference definitions | CommonMark | skip zone (below) |
| Footnote definitions (`[^label]:`) | GFM | body reflows; continuations indented (below) |
| Hard line breaks | CommonMark | immovable boundary (below) |

**Spike M0 outcome** (details and evidence in [m0-spike-findings.md](m0-spike-findings.md)): goldmark does not parse MDX, and many dialect constructs do land as `Paragraph` nodes — but a general line-scanning pre-pass is *not* needed.
The block-map derivation step grows content-pattern rules instead: (1) skip whole `Paragraph` nodes whose text matches a marker regex (`:::` fences, block shortcodes, block `{expr}`, `+++` TOML front matter); (2) within a `Paragraph`, treat any `Lines()` segment matching a marker regex as an immovable boundary — this handles fences fused with prose by lazy continuation (no-blank-line `:::`, `> [!NOTE]` markers, paired shortcodes, `$$` math); (3) recognize YAML front matter ourselves via a byte-range pre-scan (originally goldmark-meta, replaced — see Dependencies); (4) inline `{expr}`/shortcodes/math are invisible to the AST (plain `Text`) and need the already-planned regex scan for no-break spans.
The one construct post-parse rules cannot recover is a multi-line JSX opening tag whose closing `>` sits alone on a line (goldmark consumes it as an empty blockquote) — documented as a known limitation, revisit with a narrow raw-line fence if it shows up in real content.

### Sentence segmentation

Regex/punctuation splitting plus an abbreviation exception list — the approach every shipping tool uses (flowmark, mdslw, rumdl), and the approach whose known failure modes are documented in their issue trackers, which double as our test spec.
The flowmark #68 lesson is baked in: sentence-terminal punctuation must be recognized through trailing inline markup (`` `code`. ``, `**bold**.`, quotes, parens).

Segmentation is behind a public interface so it is independently testable and swappable (an NLP segmenter, or something smarter, can be plugged in without touching the pipeline):

```go
// Breaks returns the whitespace gaps separating sentences: for each
// boundary, the [start,end) byte range of the inter-sentence whitespace.
type Segmenter interface {
    Breaks(text string) []Span
}

// Span is a concrete public struct — deliberately not an alias to the
// internal type — so its fields render on pkg.go.dev; Format adapts it
// at the Segmenter boundary (one copy per cluster, custom Segmenters only).
type Span struct{ Start, End int }
```

Option enums (`Mode`, `HardBreakStyle`, and future `Dialect`) have exactly one definition, in the internal leaf package `internal/opts`, which both the root package and the pipeline packages import; the root's exported names are aliases, so the former hand-mirrored copies bridged by unchecked casts (go-quality review S4) cannot exist.

The built-in segmenter ships with a solid default abbreviation list; `Options.Abbreviations` (and the config file) *add* to it.
Wholesale replacement means providing your own `Segmenter`.

### Whitespace and hard breaks

The formatter, not the segmenter, owns whitespace at boundaries:

- At a sentence break, the entire inter-sentence whitespace run is consumed and replaced by the newline. mdreflow never emits trailing whitespace — which matters because trailing double-space is Markdown's hard-break syntax.
- On joins, inter-sentence whitespace normalizes to a single space (required for idempotency; also makes two-spaces-after-period typing invisible).
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

### Typography: removed (2026-08-09)

mdreflow shipped opt-in smart-quote and ellipsis substitution through v0.1.4 and removed it, deliberately.
Typography was the *only* transformation whose purpose was to change rendered output, which made it structurally at odds with everything else here: it forced the render oracle (and now the render backstop) to normalize substitutions back out of both sides — looseness real corruption could hide behind; it was the sole reason for the fuzz harness's one documented scope gate (the caret-zone/typography interaction); it required its own hand-mirrored context scanner to avoid substituting inside HTML attributes and link destinations (issue #3); and it was one of only two byte-rewriters in the pipeline.
Removing it makes the invariant exact: **mdreflow never changes what a document renders to** (hard-break spelling normalization and the flag-reversible `StripSentenceTerminalBreaks` change source spelling, not rendering semantics, and remain the documented nuances).

Substitution at render time is the better home for the feature — goldmark's Typographer, Hugo's smartypants, and Python-Markdown's smarty all do it without touching the source — and anyone who wants it baked into source bytes wants a full parse-and-re-emit formatter, which mdreflow is deliberately not.
A leftover `typography:` config key is a loud config error (the YAML parse is strict), never a silent no-op.

### The link-reference-definition zone: skip bluntly, by shape

A link reference definition (`[label]: /url "title"`) renders nothing — it is
URL metadata — and its grammar is the least reflow-compatible construct in
CommonMark: the label, destination, and title may each span onto following
lines, a titleless one-line definition can absorb a title from the paragraph
after it, and goldmark reorders definition nodes relative to paragraph
siblings. A first implementation grew six interlocking adjacency guards
(self-completeness reparses, registry-diff title-absorption checks, byte-level
titleless backstops, neighbor-spelling symmetry rules) and fuzzing kept
finding a seventh shape. The lesson: precision here buys reflow of prose that
is rare, ambiguity-laden, and worthless to reflow next to invisible metadata.

The rule is now deliberately blunt and shape-based: **any paragraph that
contains a non-footnote `[label]:` shape — original, reflow-escaped
(`\[label]:`), or reflow-joined (`text [label]:` at line end) spelling — or
that sits below such a line within the same contiguous run of non-blank
lines, passes through byte-for-byte.** Below-in-the-same-run, not merely
directly adjacent: a definition's title scan alone can span any number of
lines, so a paragraph several lines under the `[label]:` opener can still be
the next text the definition's own scan touches (fuzz-found; a blank line
terminates the scan and resets the run). The transitive form is also what
makes verdicts stable: everything in a def-containing run is frozen, so the
lines a verdict keys on cannot move between passes. No parsing, no
adjacency analysis, one predicate plus one precomputed per-line bit. In the
common real-world layout (definitions in their own blank-line-separated
block) nothing changes: those were already skipped.

**Footnote definitions are exempt and keep reflowing.** The caret is a
near-perfect discriminator (`[^label]:` vs `[label]:` — "near": a caret
label that is empty or starts with a space is not a footnote to goldmark
but an ordinary definition labeled `^…`, and is classified with the
definitions, fuzz-found), and the content profile is the
opposite: a footnote body is real prose. Two protections replace the guard
pile for the caret case:

- Emission escapes, label-shape-agnostic: an output line that would itself
  parse as a complete definition (judged empirically — the line is parsed in
  isolation by goldmark, not matched against a hand-mirrored grammar) or as a
  bare `[label]:` opener is backslash-escaped, so reflowed footnote prose can
  never be swallowed into an accidental definition.
- Reflowed footnote-body continuation lines are emitted with a 4-space indent.
  Renderers disagree about whether an unindented lazy-continuation line belongs to the footnote (GitHub's documented convention is to indent); the indented spelling is the one they all keep inside the footnote, and to mdreflow's own parser it is an ordinary paragraph continuation (indented code cannot interrupt a paragraph), so render preservation is unaffected.

Residual adversarial corners in the caret zone are owned by the emission escapes and the convergence backstop, not by further adjacency guards.
(The worst such family — typography substitution verdicts flipping next to `[^label]:` shapes built out of quote soup — vanished with typography's removal, taking the fuzz harness's one documented scope gate with it.)

### Spanning-construct guards: skip only what can actually span

A CommonMark inline link's label and destination can each span a soft line break, so reflow's line-joining and line-splitting can change what they parse to.
Two preparation steps and three whole-paragraph skip arms defend this, each scoped to the shape that can actually start the hazard:

**Linkify pre-check.** A paragraph with a backtick inside a GFM-linkify-eligible bare URL is skipped outright before anything else runs: linkify consumes such a backtick into the link destination, so every later backtick pairs one delimiter out of step with this package's own code-span scanning, which deliberately does not model linkify's grammar (scheme/`www.`/email forms plus trailing-punctuation trimming — a hand-mirror this codebase refuses to attempt; the skip costs only paragraphs with a backtick inside a bare URL, near-nonexistent in real prose).
This check must stay ahead of the masking step below, which shares the same blind spot — `maskCodeSpans`'s doc comment and `TestMaskCodeSpansRequiresBareURLGuard` pin the ordering (issue #28).

**Code-span masking.** The three arms scan a copy of the paragraph with every closed inline code span's interior replaced by filler bytes (same byte geometry, CommonMark's run-length pairing rule). A bracket or paren inside a code span is literal and can open nothing, so it arms no guard — paragraphs that merely *document* Markdown or YAML syntax (`` `runs-on: [self-hosted,` `` wrapping across a line) reflow. An unmatched backtick run opens no span and masks nothing, so a bracket after one still arms normally.

- **Bracket arm**: a `[` left structurally open at a line's end (even one
  a later line closes — the span across the break is itself the hazard),
  but only in a paragraph that also contains a `]:` — on one line, or as
  `]` ending one line with `:` opening the next — the only shape a link
  reference definition can be built from. Without it, no rearrangement of
  the lines can form a definition (CommonMark permits a newline inside
  every other bracket construct's label or text, and label matching
  collapses internal whitespace), so a wrapped link is safe to reflow —
  and needs to be, since a skipped-because-wrapped paragraph is otherwise
  a permanent fixed point: the skip preserves the very break that causes
  it.
- **Destination-paren arm**: a `(` left open at a non-final line's end
  skips the paragraph only if that specific paren was opened by `](` — the
  only spelling that opens an inline destination, since CommonMark admits
  no whitespace between a link's `]` and its `(`. Each open paren carries
  its own armed flag (a stack, not a depth-0 check: the hazard nests
  inside ordinary prose parens). An ordinary prose parenthetical spanning
  a line break arms nothing and keeps reflowing regardless of what
  brackets appear elsewhere in the paragraph.
- **Angle-destination arm**: `](` then optional spaces then `<` with no
  `>` on the same line — joining can complete it into a real
  angle-bracket destination.

Definition titles in parens (which may also span lines) are caught upstream by the link-reference-definition zone, not by these arms.

### Control-character paragraphs pass through

A paragraph containing a C0 control byte other than tab (form feed, vertical tab, a bare carriage return outside a CRLF pair, ...) passes through byte-for-byte.
No text editor produces these inside prose; they exist in fuzz inputs, where they sit in exactly the grammar corners (indentation width, whitespace-class membership) that differ between parser implementations.
Reflowing around them buys nothing for real documents and costs a long tail of corner-case hardening.
CRLF line endings are unaffected (the `\r` of a CRLF pair is line-ending machinery, not paragraph interior).

## Guarantees

Stated as testable promises, enforced by the harness in [Testing](#testing).

**Input domain: valid UTF-8.** `Format` rejects invalid UTF-8 with the typed error `ErrInvalidUTF8` and writes nothing; the guarantees below are stated over inputs it accepts.
(The CLI already refused such files via its binary sniff, but the sniff only reads the first 8 KB — the library check is total.
Markdown is text; reflowing bytes that have no character interpretation was never meaningful, and fuzzing spent most of its findings on inputs no user could produce with a text editor.)

1. **Idempotency.** `Format(Format(x)) == Format(x)` for every accepted input and option set.
   This is structural, not aspirational: `Format` runs to fixpoint and falls back to the original text for anything that will not converge (see [Convergence](#convergence-reflow-runs-to-fixpoint)).
2. **Render preservation.** Reflow does not change the rendered document, verified by comparing goldmark HTML output before and after.
   Documented exceptions: hard-break style normalization (renders the same `<br>`, different source) and `StripSentenceTerminalBreaks`.
3. **Byte-identical pass-through.** Everything outside reflowed paragraph prose is emitted byte-for-byte.
4. **Check mode.** `--check` and `--diff` report unformatted files without writing, with a stable exit-code contract for CI and agents.

## Library API

Library-first: the root package is the product, `cmd/mdreflow` is a thin wrapper, internals live in `internal/`.
The core is pure bytes-in/bytes-out; file discovery, config loading, excludes, and input detection are CLI-layer concerns (promoting them into the library later is non-breaking; the reverse is not).

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
    MaxWidth      int            // 0 = unbounded; otherwise >= MinMaxWidth (20); on ModeSentence enables secondary clause breaks
    HardBreaks    HardBreakStyle // default: HardBreakBr
    StripSentenceTerminalBreaks bool
    Abbreviations []string       // additions to the built-in list
    Segmenter     Segmenter      // nil = built-in
}

func Format(src []byte, opts Options) ([]byte, error)
func NeedsFormat(src []byte, opts Options) (bool, error)

// Invalid UTF-8 input; returned (wrapped) by Format/Check/FormatReader.
// Callers branch with errors.Is.
var ErrInvalidUTF8 = errors.New("input is not valid UTF-8")

// Convenience wrapper; buffers the full input (Markdown is not streamable —
// e.g. a reference definition on the last line affects the first).
func FormatReader(dst io.Writer, src io.Reader, opts Options) error

func DefaultAbbreviations() []string
```

Every zero value is the default (`Options{}` is valid and sensible): zero `Mode` is sentence mode, zero `MaxWidth` is unbounded, zero `HardBreakStyle` is `<br>`.

**The width floor** (amendment 2026-08-09): a non-zero `MaxWidth` below `MinMaxWidth` (20) is an options error, in the library and the CLI alike.
Nearly every pathological width finding from the fuzz campaign needed single-digit widths, where geometry *forces* breaks inside constructs; no human asks for width 12.
Refusing the range deletes that adversarial surface from the product without weakening the harness: like the convergence backstop, the floor has a test-only disable so the fuzzer and the narrow-width fixtures keep driving the unrestricted core, and a tiny-width find is still root-caused (some generalize to legal widths with long tokens) — it just can no longer be a user-facing bug on its own.
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
- **Flags**: `--mode`, `--max-width`, `--check`, `--diff`, `--stdout`, `--force`, `--config`, `--no-gitignore`, plus flags mirroring the hard-break options.
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

Candidate libraries (implementation-time decision; the behavior is the commitment, the library is a detail): [boyter/gocodewalker](https://github.com/boyter/gocodewalker) for tree walking that honors nested `.gitignore`, and [sabhiram/go-gitignore](https://github.com/sabhiram/go-gitignore) for matching config patterns.
Caveat: no Go gitignore implementation is fully spec-compliant (see [go-git#108](https://github.com/go-git/go-git/issues/108)), so the test suite compares our matching against `git check-ignore` on a synthetic tree.

### Non-Markdown input detection

The observed failure mode is an unattended agent running the formatter on a YAML or JSON file.
V1 defense, deliberately cheap:

- **Extension check** on explicitly named files: known-Markdown extensions pass; known-other extensions (`.yaml`, `.json`, `.go`, …) are refused.
- **Binary sniff**: NUL bytes or invalid UTF-8 in the first 8&nbsp;KB → refused.

Refusal is exit code `3` with a one-line explanation and no write; `--force` overrides.
Refusing (vs. warning) is the point: a warning in scrolled-past output doesn't stop an unattended agent, a non-zero exit does.
Directory walks are unaffected (they only pick up Markdown extensions).
Content-based sniffing for unknown extensions ("parses as YAML/JSON with no Markdown block constructs") is deferred until mislabeled files are observed in practice — the heuristic's second half is fuzzy, and the extension check covers the failures actually seen.

## Testing

The wrapping logic is heuristic; a deep test corpus is the only durable defense against regressions, and the guarantees above are only as real as this harness.

1. **Golden-file fixtures** (`testdata/`): input → expected output pairs covering every skip-list construct, every mode, every option.
   Fixture content is synthetic (lorem-ipsum-style or purpose-written), reproducing *structures* observed in real repos without lifting content.
2. **Property tests over the corpus**: idempotency and render preservation (goldmark HTML before == after, documented exceptions aside) run automatically over every fixture — adding a fixture adds the property checks for free.
3. **Mined regression cases**: bugs from flowmark's, mdslw's, and rumdl's issue trackers re-authored (not copied) as fixtures — e.g. flowmark #68's `` `token`. `` boundary misses.
4. **Segmenter suite**: the sentence splitter tested standalone against a Golden-Rules-style case list (in the style of [pragmatic_segmenter](https://github.com/diasks2/pragmatic_segmenter)'s golden rules, cases our own).
5. **Exclude parity**: our gitignore matching vs. `git check-ignore` output on a synthetic tree.
6. **Fuzzing**: Go native fuzzing on `Format` with crash, idempotency, and render preservation as oracles.
   Invalid-UTF-8 inputs assert only the `ErrInvalidUTF8` refusal (that path must still never panic); the reflow oracles run on accepted inputs.
   The harness has no scope gates: every accepted input runs every oracle.
   (The one historical gate — caret-def openers mixed with typography-substitutable quotes — existed only for typography and was deleted with it; see the Typography section.)

## Dependencies

Small on purpose; additions require amending this doc.

- `github.com/yuin/goldmark` (+ GFM extension) — parsing, library core
- `github.com/goccy/go-yaml` — config file, CLI layer only.
  (Amended 2026-08-07: originally `gopkg.in/yaml.v3`, which is no longer maintained; goccy/go-yaml is actively maintained with better spec compliance.)
- YAML front matter is recognized by our own byte-range pre-scan (`internal/blockmap`), not a parser extension.
  (Amended 2026-08-07: this was goldmark-meta at first, but mdreflow never consumes the parsed metadata — it only needs the block excluded from reflow — and goldmark-meta's YAML parsing dragged in unmaintained `yaml.v2` plus parser-hook artifacts the fuzz harness had to defend against.
  Opener is exactly `---` at byte 0; unterminated front matter is not front matter.)
- `github.com/boyter/gocodewalker` — CLI layer only; used solely for its vendored `go-gitignore` subpackage (nested-`.gitignore` semantics).
  The walk itself is hand-rolled `filepath.WalkDir` (M4 decision; sabhiram/go-gitignore was evaluated and dropped — no nested-file support).
- `github.com/pmezard/go-difflib` — CLI layer only, unified diffs for `--diff` (M4 addition; hand-rolling Myers diff wasn't worth the risk).
- Stdlib `flag` for the CLI (single command; cobra buys weight, not function; the cost is shell completions, acceptable for now)

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
- **Per-file options in front matter** — a reserved key (e.g. `mdreflow: {mode: para}`) slotting into the precedence chain as flags > front matter > config > defaults.
- **`--dialect` flag** — subsets of the tagged skip-list rules.
- **Content sniffing for unknown extensions** — gated on observed need.
- **GitHub Action, Homebrew tap, editor extensions** — distribution beyond goreleaser + pre-commit.
- **Alternative segmenters** — NLP (e.g. jdkato/prose) or model-based splitting behind the `Segmenter` interface, if the regex approach's ceiling is ever actually hit.
