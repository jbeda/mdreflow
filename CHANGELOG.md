# Changelog

Release notes for mdreflow, newest first.
Each release leads with a short prose summary of what matters — themes, breaking changes, things to act on — followed by grouped details.
The raw commit list is always available via the compare link at the end of each release's notes on GitHub.

Maintenance rule: user-visible changes add a line to the Unreleased section in the same commit.
At release time the section is retitled to the version and the prose lead is written; see RELEASING.md.

## Unreleased

- Fixed: `--hard-breaks=spaces` no longer silently drops a hard break on a checked/unchecked task-list item's first line when the line is just the checkbox (e.g. `- [x] <br>`) — mdreflow now falls back to the backslash marker in that one case, since the spaces marker would otherwise be swallowed by task-list parsing.
- Fixed: `--hard-breaks=backslash` no longer emits an unreadable hard break when prose already ends in one or more backslashes (e.g. an escaped literal backslash) — mdreflow now falls back to `<br>` in that one case, since gluing another backslash on would produce a run the Markdown parser does not read back as a break.

## v0.1.7 (2026-08-11)

When mdreflow declines to reflow a paragraph, it can now tell you why.
The new `--explain` flag prints one record per frozen paragraph — file and line range, a stable machine-readable reason code, and a remediation hint — so authors get the fix for their exact case (usually: move the literal into a fenced code block) and agents get coverage measurement in one pass instead of hand-classifying diffs.
On the 745-file real-world docset used to validate this release, 98.6% of paragraphs reflow, and every frozen one now names its reason.

Coverage also improves where the old rule was bluntest: prose that merely quotes a `[label]:`-shaped error message in inline code no longer freezes the paragraph below it as definition machinery — the freeze now requires a definition chain that could actually reach the shape (#37, reported by Karl Isenberg with the fuzzing evidence that drove the fix).

Two correctness fixes land where a hand-written rule disagreed with the Markdown parser; both were fuzz-found, and both now defer to the parser's own judgment.

Nothing you must act on: no breaking changes, no changed defaults; one new flag.

### Added

- New `--explain` flag (#38): reports every paragraph mdreflow leaves unformatted to stderr — location, a stable machine-legible reason code, and a remediation hint.
  Combines with every mode and never changes output or exit codes.
  Also available as a library call, `mdreflow.Explain`.
- New doc: [docs/why-this-is-hard.md](docs/why-this-is-hard.md) explains why safe Markdown reflow is hard and what to do when a paragraph will not reflow.

### Changed

- A paragraph sitting directly under a line whose only `[label]:` shape is mid-prose (typically a quoted error message in inline code) now reflows; a definition opening after a list marker (`- [a]: /url`) now freezes its whole run, the same as a top-level definition (#37).

### Fixed

- Trailing double-space and trailing-backslash line endings are now treated as hard breaks only when the Markdown parser agrees they render as one (#39).
  Previously a task-list line whose prose is just the checkbox (`* [X]` followed by two trailing spaces) had those spaces read as a hard break even though it renders as a soft one, so the paragraph was left unformatted; it now reflows.
- A footnote-shaped paragraph (`[^label]: …`) sitting directly under link-reference-definition machinery — a `[label]:` line or a bare `[^label]:` opener — no longer reflows (#41): joining its lines could change whether the definition above it forms at all.
  The ordinary back-to-back footnote layout is unaffected and keeps reflowing.

## v0.1.6 (2026-08-10)

More of your prose reflows, because the tool stopped guessing.

The rules deciding where a line may not break — inside inline code, a link, a bare URL — used to be hand-written approximations of Markdown's inline grammar, and the hardest one, GitHub's bare-URL autolinking, was never approximated at all: paragraphs near a URL were skipped outright rather than risk getting it wrong.
Those rules now come from the same parser that renders the document, so the answer agrees with the renderer by construction.
The practical effect is coverage.
A code span that ends on a URL — `` `curl https://example.com/install.sh` ``, how documentation writes a command — no longer stops its paragraph from reflowing; on a 263-file docset that shape alone accounted for 91 of the 117 lines that still would not reflow.

Nothing you must act on: no flags, config keys, or API changes.
Output for a paragraph that already reflowed is unchanged except where a fix below applies.

### Changed

- More prose now reflows near bare URLs: a paragraph that contains both a URL and a stray backtick is no longer skipped wholesale (#30).
  The spans reflow must not break inside — inline code, links, autolinks — are now read from the same Markdown parser that renders the document, so bare-URL autolinking is handled exactly as the renderer handles it instead of being approximated and then guarded against with a conservative skip.

### Fixed

- Wrapping list-item prose so that `**`-style text lands on the marker line of a nested list no longer lets the result reparse as a thematic break that destroys the list; the escape now judges the line the way a reparse consumes nested container markers (`0) * ` and the like).
- A paragraph containing a raw `<?…?>` processing instruction or `<!…>` comment/declaration outside inline code now passes through untouched instead of reflowing unstably; the same constructs inside backticks keep reflowing normally.
- A run of repeated same-label footnote-style definitions (`[^0]: …` twice, then prose) no longer keeps re-joining on every pass; such paragraphs now pass through untouched (#35).
- A MkDocs admonition written without a blank line after its marker is no longer flattened into one paragraph, which stopped it rendering as a callout (#31, contributed by Karl Isenberg).
  That spelling is a single paragraph to CommonMark, since the indented body is a lazy continuation rather than the code block the blank-line spelling produces.
  The marker line is now immovable and the body carries the 4-space indent the extension requires, the same treatment a footnote definition already gets.
- Reflowing a tab-indented list-item body no longer risks turning it into a thematic break (#32, found by Karl Isenberg).
  Two emphasis-only lines like `**` and `**` join to `** **` — four asterisks, which CommonMark reads as a thematic break — and the tab indent hid that from the escape that would otherwise neutralize it, so the join silently ended the list and changed what the document rendered to.
  The body is now escaped and reflows normally.

## v0.1.5 (2026-08-09)

Narrower promises, structurally enforced.
The core guarantee — reflow never changes what a document renders to — is now checked at runtime, not just in tests: `Format` compares renders before and after and returns your document untouched rather than ever shipping a difference.
Around that, the tool's surface got deliberately smaller and sharper: typography substitution is gone (your renderer does it better), very narrow `--max-width` values are refused, and the Markdown flavor is now an explicit `--dialect` with `gfm` as the default and an opt-in `mkdocs` that reflows admonition bodies.
Running unattended on untrusted trees is hardened end to end, and four guard fixes contributed by Karl Isenberg stop link- and URL-adjacent paragraphs from being silently skipped — on his 263-file docset, sentence-per-line coverage went from 96% to over 99%.

Act on these when upgrading:

- The library function `Check` is now `NeedsFormat` (same behavior; the old name read backwards).
  The `--check` flag is unchanged.
- A `typography:` key in `.mdreflow.yaml` is now an error — delete the line and configure typography in your renderer instead.
- `--max-width` below 20 is now an error; the library requires Go 1.25+.

### Added

- Render preservation is now structurally guaranteed, not just tested for: after reflowing, `Format` renders the input and the output through the same parser and compares them (modulo soft-break whitespace and `<br>` spelling — the two documented cosmetic differences).
  On any other difference the document is returned unchanged, so an unknown formatter bug can now cost you a reflow, never your content.
  The opt-in `--strip-sentence-terminal-breaks` remains the one documented exception, since removing an accidental hard break is a render change by design.

- `--dialect mkdocs` (and a `dialect:` config key) additionally reflows MkDocs and Python-Markdown admonition bodies (#19, contributed by Karl Isenberg).
  A callout's 4-space-indented body is prose, but every CommonMark parser reads it as an indented code block, so it was silently excluded from reflow.
  Off by default: to a CommonMark renderer that body is code, so reflowing it changes the rendered `<pre><code>`.
  Recognition is narrow, skipping bodies that contain a fence marker or more than one paragraph.
  The default dialect is named `gfm` — the GitHub-flavoured superset the parser has always used; `commonmark` is reserved for a possible future strict profile.

### Changed

- Running unattended on an untrusted tree is now hardened end to end: symlinks, FIFOs, and device nodes are refused (exit 3) instead of read or written through — a directory walk skips symlinks silently, `--force` remains the escape hatch; in-place writes go through a same-directory temp file and atomic rename, so a crash or full disk mid-write can no longer truncate a file; a discovered `.mdreflow.yaml` is capped at 1 MB and screened against YAML alias/nesting bombs before parsing; and config discovery stops at the enclosing git repository root (or your home directory outside one), so a config planted in a shared ancestor directory like `/tmp` no longer applies.
- `--no-gitignore` outside a git repository no longer misfires the built-in `vendor`/`node_modules`/`.git` excludes on directories *above* the path you named: the check is scoped to components below the walked directory (or an explicit file's own directory), not the full absolute path.
- `--max-width` (and the library's `Options.MaxWidth`) now rejects non-zero values below 20 with a loud error.
  Very narrow widths force line breaks inside Markdown constructs and were the source of nearly all fuzz-found width pathology; no real document wants them.
  0 still means unbounded (sentence mode) or the default 80 (wrap mode).

- Library API: `Check` is renamed `NeedsFormat` — `ok, err := Check(...)`, the shape a Go reader expects from that name, read exactly backwards (`true` means the file *would* change).
  The `--check` flag keeps its name; it describes the operation, the function describes the question.
  No deprecation wrapper: renaming now, pre-v1, is the cheap moment.
- `Mode`, `HardBreakStyle`, and `Dialect` now implement `fmt.Stringer` with their CLI-facing names ("sentence", "gfm", "br", ...), so validation errors and logs speak the same vocabulary as flags and config keys.
- Library API, pre-v1 cleanup: `Span` is now a concrete struct whose `Start`/`End` fields appear in rendered documentation (it was an alias to an internal type that pkg.go.dev showed with no fields), and the option enums have a single internal definition instead of hand-mirrored public/internal copies.
  No source changes needed for typical callers; a custom `Segmenter` implementation is unaffected beyond recompiling.

- The library now requires Go 1.25+ (was 1.24), following a dependency update; releases build with the current Go toolchain.

### Removed

- Typography substitution (`--smart-quotes`, `--ellipses`, the `typography:` config key, and the library's `Typography` options) is removed.
  It was the only feature whose purpose was to change rendered output, which put it at odds with mdreflow's core guarantee; substituting at render time is the better home (goldmark's Typographer, Hugo's smartypants, Python-Markdown's smarty), and anyone wanting it baked into source bytes wants a full parse-and-re-emit formatter, which mdreflow deliberately is not.
  A leftover `typography:` key in `.mdreflow.yaml` is now an unknown-key config error (exit 2); delete the line.

### Fixed

- Trailing double spaces inside a code span that closes on a later marker line (a `:::` fence, say) are no longer treated as a hard break: they are span content, and normalizing them wrote a literal `<br>` into the rendered code.
  Fuzz-found on pathological input; v0.1.4 renders it corrupted, while the render backstop above already reduced it to a missed reflow before this root fix.
- A paragraph is no longer skipped just because a code span contains a URL (`` `oci://ghcr.io/org/chart` ``), which is how documentation names a registry or endpoint (#29, contributed by Karl Isenberg).
  The backtick-in-bare-URL guard now requires the URL to start *before* the backtick, which is the only order in which linkify can swallow one.
- A paragraph whose link text or destination spans a line break is no longer skipped unless the paragraph could actually form a link reference definition (#18, contributed by Karl Isenberg).
  The bracket guard now also requires a `]:` somewhere in the paragraph, the only shape a definition can be built from.
  Such a paragraph was otherwise a fixed point: skipped because a link spans a break, and being skipped is what stopped the break from ever being removed.
- A bracket inside an inline code span no longer counts toward the spanning-delimiter safety guards, so paragraphs that document Markdown or YAML syntax (`` `runs-on: [self-hosted,` `` wrapping across a line) reflow instead of being skipped (#17, contributed by Karl Isenberg).
- Paragraphs containing a link are no longer skipped just because a prose parenthetical spans a line break elsewhere in them (#16, reported with a proposed fix by Karl Isenberg): the guard now fires only on a `](`-opened paren left open at a line end — the one spelling that can open an inline link destination — instead of any unclosed paren in a paragraph that contains a `[` anywhere. On the reporter's 263-file docset this unblocks 137 files.
- More scanner whitespace classes aligned with goldmark's, which treats a bare carriage return as whitespace where the spec says space/tab: table-delimiter-row and setext-underline recognition, two link-reference-definition opener shapes, and hard-break `<br>` marker detection.
  Fuzz-found on pathological input (paragraphs containing a bare CR); no effect on normal documents.
- A width split inside a list item nested in a blockquote could land an asterisk run right after the `* ` marker, which the next parse read as a thematic break (`>* **` is `* **` once the quote marker is stripped — a real `<hr>`); the escape that already defended this for plain list items now sees through blockquote markers.
  Fuzz-found at width 3 on adversarial input; real documents are unaffected.
- `[^]:` and `[^ ]:` (a caret label that is empty or starts with a space) are ordinary link-reference definitions to goldmark, not footnotes; they now get the definition zone's protection instead of the footnote exemption, closing a fuzz-found corner where joining an adjacent paragraph turned its prose into the definition's title.
- A paragraph containing a backtick inside a bare URL now passes through unchanged. GitHub-style autolinking absorbs such a backtick into the link, which shifts how every later backtick in the paragraph pairs into code spans; mdreflow's own scanner did not model that and could break a line inside a real code span, changing the code's rendered content.
- The definition zone now extends through a whole contiguous run of non-blank lines below a `[label]:` line, not just the line directly against it: a definition's multi-line title scan can reach a paragraph several lines down, and reflowing that paragraph re-carved the title's boundary on the next pass. Definitions in their own blank-line-separated block — the normal layout — are unaffected.

## v0.1.4 (2026-08-08)

Hardening and simplification.
A day-long fuzzing campaign fixed the whole family of edge cases where reflowed output could parse differently than the input — splits manufacturing accidental tables, definitions, or headings; escapes re-pairing code spans; typography flipping raw-HTML recognition.
All were pathological shapes that never occur in normal prose; each is now pinned by a regression seed.
Alongside the fixes, some deliberately over-clever handling was descoped in favor of simpler, safer rules.

### Fixed

- Two field reports from real docsets: hard-wrapped prose with a parenthetical spanning a line break is no longer silently skipped (#14), and a valid UTF-8 file is no longer refused when a multi-byte character straddles the 8 KB detection boundary (#15).
- Roughly twenty fuzz-found idempotency and render-preservation corners (#4–#8, #10–#13 and successors), none affecting normal documents.

### Changed

- **Formatting is now guaranteed stable**: `Format`'s output is a fixpoint — running the tool twice never produces a second diff, even on adversarial input.
- **Invalid UTF-8 is rejected outright** with the typed error `mdreflow.ErrInvalidUTF8` (CLI: exit 3, even under `--force`); Markdown is text, and bytes with no character interpretation have nothing meaningful to reflow.
- **Simpler scope around link reference definitions**: any paragraph containing or directly touching a `[label]:`-shaped line now passes through unchanged, replacing subtle partial handling. Definitions in their own blank-line-separated block — the common layout — were already skipped, so typical documents are unaffected.
- **Footnote bodies** (`[^label]: ...`) still reflow as ordinary prose, and their continuation lines are now indented four spaces — the one spelling every Markdown renderer keeps inside the footnote.
- Paragraphs containing control characters (form feed, vertical tab, …) pass through unchanged.

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
