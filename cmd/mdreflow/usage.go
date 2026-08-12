package main

import (
	"flag"
	"fmt"
	"io"
)

// printUsage is mdreflow's --help output. It is a first-class deliverable
// (docs/design.md): an unattended agent should be able to use the tool
// correctly from this text alone, without a README — complete flag
// docs, the exit-code contract, a config-file summary, and worked
// examples.
func printUsage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprint(w, `mdreflow reflows Markdown prose: it changes where lines break inside
paragraph text and leaves everything else in the document untouched (no
heading, list, table, or escaping changes). Its home mode is
sentence-per-line (one sentence per source line, aka semantic line
breaks); see https://sembr.org/.

Usage:

  mdreflow [flags] [path ...]
  mdreflow [flags] < in.md > out.md

Paths:

  With one or more path arguments, mdreflow formats in place: each file
  argument is formatted directly, and each directory argument is walked
  recursively for Markdown files (.md, .mdx, .markdown). Excludes (see
  below) apply to files discovered by a directory walk (skipped
  silently) AND to files named explicitly on the command line (skipped
  loudly, with an exit-3 refusal, unless --force is given).

  With no path arguments, or a single "-", mdreflow reads Markdown from
  stdin and writes the formatted result to stdout: a pipe/editor-filter
  mode with no in-place writing, no excludes, and no config-file
  discovery from a target file (config is discovered from the current
  directory instead). --check and --diff work here too, reporting the
  input as "-".

Flags:

`)
	// PrintDefaults writes to fs.Output(), not w; retarget it so the whole
	// help text lands on one stream (stdout for --help), then restore.
	prev := fs.Output()
	fs.SetOutput(w)
	fs.PrintDefaults()
	fs.SetOutput(prev)
	fmt.Fprint(w, `
Exit codes (a contract other tools and agents can branch on):

  0  success — nothing needed to change (or, with --check/--diff,
     nothing would change)
  1  --check or --diff found at least one file that would be
     reformatted; nothing was written
  2  usage or config error (bad flags, bad flag combination, an
     unreadable or invalid path argument, a .mdreflow.yaml that fails
     to parse or has an unknown key, an internal formatting error).
     Aborts the run immediately — no files after the failing one are
     processed.
  3  at least one input was refused: excluded (gitignore, config
     exclude:, or a built-in .git/node_modules/vendor exclude) without
     --force, or not recognized as Markdown (wrong extension, binary
     content, or invalid UTF-8) without --force, or not a regular file
     (a symlink, FIFO, device, or socket named explicitly on the
     command line) without --force. A symlink found while walking a
     directory is skipped silently instead, same as any other walk
     exclusion. Other targets in the same run are still processed; a
     refusal only raises the run's final exit code.

  When more than one of these would apply in a single run, the most
  severe wins: 2 > 3 > 1 > 0. In practice this means a config/usage
  error stops everything immediately, while a per-file refusal among
  several files still lets the rest of the batch be checked or
  formatted before the run reports exit 3. The numbers follow Unix
  convention (diff-style 1 for "differences found", 2 for usage errors,
  as grep and diff use), so severity deliberately does not track
  numeric order.

Configuration (.mdreflow.yaml):

  Discovered by walking upward from each target file's directory (from
  the current directory for stdin), or read directly with --config.
  Discovery stops at the enclosing git repository's root, or at your
  home directory if the target isn't inside a repo, so a config in an
  unrelated shared ancestor directory can't silently apply. Precedence
  is flags > config file > built-in defaults; a flag explicitly given
  on the command line wins even if its value equals the default.
  Unknown keys are a loud error (exit 2) rather than a silent no-op — a
  typo'd key should not be ignored. A .mdreflow.yaml over 1 MB, or one
  engineered with pathological bracket nesting or YAML alias chains, is
  refused (exit 2) rather than parsed.

    mode: sentence          # sentence | para | wrap
    dialect: gfm            # gfm | mkdocs (mkdocs also reflows admonition bodies)
    max-width: 0            # 0 = unbounded/default; otherwise >= 20
    abbreviations:          # additions to the built-in list
      - "et al."
    exclude:                # gitignore syntax, matched like a .gitignore
      - "CHANGELOG.md"
      - "generated/**"

  The default dialect, gfm, is the permissive GitHub-flavored superset:
  GFM extensions plus footnotes. --dialect mkdocs
  additionally reflows MkDocs/Python-Markdown admonition bodies
  ("!!! note" plus a 4-space-indented body), which CommonMark parsers
  read as code blocks — opt-in because that changes what a CommonMark
  renderer emits. "commonmark" is not accepted as an alias for gfm: the
  name is being kept for a possible future strict profile, so passing it
  is an error that says exactly that.

  These are the only config keys; --explain is flag-only. typography:
  and hard-breaks: are not among them, and a config carrying either is
  an unknown-key error: delete the key. Hard-break spelling follows the
  source rather than configuration: a trailing double-space is promoted
  to a backslash (double spaces are invisible and get stripped in
  transit), every other spelling is kept as written, and raw HTML
  ("<br>") is never introduced. Under --dialect mkdocs the promotion
  does not happen — Python-Markdown has no backslash hard break and
  renders one as a literal "\" — so a double-space is kept as-is. For
  quote and ellipsis substitution use a render-time option such as
  goldmark's Typographer.

Explain (--explain):

  Some paragraphs are deliberately left unformatted: constructs like
  link-reference-definition shapes make reflow unsafe, so mdreflow
  preserves those paragraphs byte-for-byte (see docs/why-this-is-hard.md
  in the repository). --explain reports each one to stderr — stdout
  stays clean for formatted output and diffs — as one record per frozen
  paragraph:

    docs/webhook.md:14-15: skipped: paragraph contains a "[label]:"
    shape (link-reference-definition zone) [link-ref-def-shape]
      Move the literal into a fenced code block, or format the
      paragraph by hand -- mdreflow preserves it byte-for-byte.

  The bracketed reason code is stable and machine-legible; the
  indented line is the remediation hint. --explain combines with every
  mode (in-place, --check, --diff, --stdout, stdin) and never changes
  output bytes or exit codes. No records means nothing was frozen.

Excludes:

  Checked in this order, first match wins: the built-in always-excludes
  .git/, node_modules/, vendor/, then the repository's .gitignore files
  (nested ones included; disable with --no-gitignore), then the config
  file's exclude: patterns (gitignore syntax). A directory walk skips
  excluded files and directories silently. A file named explicitly on
  the command line is instead refused loudly: "<path>: skipped
  (excluded by <gitignore|config|built-in>)", exit 3, unless --force.

Examples:

  Format a file in place:
    mdreflow docs/README.md

  Format everything in a directory tree in place:
    mdreflow docs/

  Pipe mode, e.g. an editor "filter selection through command" binding
  (never redirect a file onto itself — the shell truncates it before
  mdreflow reads it; use in-place mode for that):
    mdreflow < draft.md > formatted.md

  CI check that fails the build if anything needs reformatting:
    mdreflow --check docs/ || exit 1

  See what would change without writing anything:
    mdreflow --diff README.md

  An agent formatting one file and capturing the result without
  touching disk (e.g. to review before committing):
    mdreflow --stdout notes.md > /tmp/formatted.md

  Force-format a file that excludes would otherwise refuse (e.g. a
  generated file explicitly opted back in for one run):
    mdreflow --force generated/CHANGELOG.md
`)
}
