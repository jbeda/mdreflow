# Why this is hard

`docs/design.md` says what mdreflow does.
This document explains why the problem resists simple solutions: why a tool that "just rewraps paragraphs" needs a freeze zone, emission escapes, and a fuzzing habit.
Read it before proposing to make a frozen construct reflow.
It assumes you know Markdown as a user; it does not assume you know CommonMark's parsing model.

## The three layers: block, span, cluster

CommonMark parses in two strictly ordered phases.

**Blocks come first, from raw bytes only.** Paragraphs, list items, blockquotes, headings, code blocks, and link reference definitions are determined by line shapes: markers, indentation, and blank lines.
The block pass cannot see inline constructs.
It does not know what a backtick is.

**Spans come second, per block.** Inline constructs (code spans, links, emphasis) are parsed within each block's text after block structure is settled.

The dependency is one-way, and the direction is the opposite of what intuition suggests: a code span never protects anything from a block rule.
A blank line "inside" backticks still ends the paragraph; the span simply never forms, and each half is left holding an unpaired backtick.
A `- ` at a line start still interrupts a paragraph even if the author meant it as code.

**Clusters are the emergent third layer.** A cluster is a maximal run of lines containing no blank line: a stretch of blocks that all touch.
It is not a CommonMark term, but it is the unit that matters for safety.
Every cross-block mechanism (definition title absorption, lazy continuation, paragraph interruption, adjacency-based hazards) requires direct contact.
A blank line stops all of them.
Nothing in the grammar reaches across one.

```
Intro paragraph, wraps freely.          <- cluster 1: fully independent

[logo]: /img.png                        -+ cluster 2: def, paragraph,
"Our logo" and prose continuing on       | and list touch; edits and
- first bullet                           | verdicts here are entangled
- second bullet                         -+ with each other

Closing paragraph, also free.           <- cluster 3
```

Most real documents are almost entirely single-block clusters, because authors blank-separate everything out of habit.
That is why blunt per-neighborhood freezing costs so little in practice.

## Reflow is a fixpoint problem

mdreflow edits line breaks, then the result gets parsed again: by the next mdreflow run, by the site generator, by a coworker's renderer.
Two requirements follow, in priority order (design.md covers the hierarchy):

1. **Render preservation.** The reflowed bytes must parse to the same structure and render to the same output.
2. **Idempotency.** Running mdreflow on its own output must change nothing.

Here is the trap.
The tool that decides where breaks are safe is the *inline* parser (spans mark no-break regions).
The judge of the result is the *block* pass of the next parse, which is span-blind.
The two speak different languages, and reflow's edits carry bytes from one jurisdiction to the other: a join or split moves bytes to new line-start positions, and line starts are exactly where block rules fire.

Every hard bug in this codebase is that one mismatch in different clothes.

## Delimiting is semantics

It would be convenient to firewall blocks from each other by inserting blank lines.
That fails because blank lines are not separators in Markdown; they carry meaning.

A blank line between list items flips the list from tight to loose: every item gets wrapped in `<p>` and every renderer shows the extra spacing.
An unprefixed blank line inside a blockquote splits it into two `<blockquote>` elements.
A blank line inside a paragraph makes two paragraphs.

So clusters exist precisely where blank lines are absent because they are semantically forbidden.
Wherever a blank line is free, authors already put one, and the firewall already exists.
The ambiguity lives exclusively in the places where the wall cannot be built.

## In-cluster interactions: the hard ambiguities

Concrete cases, each of which defeats an obvious rule.

### A block above can eat the line below

A link reference definition without a title absorbs a quote-led line after it as its title:

```
[logo]: /img.png
"Our logo" appears in the header and more prose follows here.
```

If rewrapping the paragraph ever puts the quoted text in absorbable position, those bytes stop being paragraph and become invisible metadata.
The paragraph's own bytes were edited legally; the neighbor ate them.
Byte ownership migrates across block boundaries on reparse.

### A sibling's safety verdict depends on your bytes

mdreflow parses the document once, computes a verdict for every block from those original bytes, then emits.
A verdict about one block is routinely computed by reading bytes that belong to another: deciding whether this bullet is safe to rewrap requires judging the line directly above it, which the previous bullet owns.

```
- The error reads
  `runnerGroups[0]: priorityClassName is not allowed` in that case.
- A tenant with direct RBAC bypasses the webhook.
```

The second bullet contains no bracket at all. Its verdict reads the
first bullet's last line, sees a `[0]:` shape there, and freezes.

**Worked trace: how a correct verdict goes stale.** Suppose a rule let the first bullet reflow (say, because its bracket shape is inside a code span).
Simplified to short words, wrapped at width 20:

```
- aaa bbb
  ccc `q[l]: u` dd ee ff gg
- hh ii jj kk ll mm nn oo pp qq
```

Run 1 computes all verdicts from these bytes. Bullet 1: eligible,
rewraps. Bullet 2: the line above it contains `[l]:`, freeze. Output:

```
- aaa bbb ccc
  `q[l]: u` dd ee ff
  gg
- hh ii jj kk ll mm nn oo pp qq
```

Run 2 executes the same rules on that output.
The line above bullet 2 is now `gg`.
Nothing dangerous there, so bullet 2's verdict flips to safe and it rewraps:

```
- aaa bbb ccc
  `q[l]: u` dd ee ff
  gg
- hh ii jj kk ll mm
  nn oo pp qq
```

Two runs, two different outputs: idempotency is broken, and no individual rule was ever wrong.
Bullet 2's verdict was correct when computed and stale by the time it mattered, because bullet 1's edit moved the evidence.

**The dependency also points the other way.** Here the danger flows downward: a lower block's rewrap changes what an upper block means.

```
[logo]: /img.png
"Our logo" is shown
in the header.
```

Today this is a titleless definition followed by a two-line paragraph: the first paragraph line fails to parse as a title (content continues after the closing quote on the same line), so the quote stays prose.
Now suppose a rewrap lands the quoted phrase alone on its first line:

```
[logo]: /img.png
"Our logo"
is shown in the header.
```

The reparse sees `"Our logo"` alone on the line after the definition's destination.
That is now a valid title, so the definition absorbs it.
The words "Our logo" vanish from the render, and the remaining paragraph reads "is shown in the header."
The paragraph's edit was locally legal; the neighbor above changed its meaning.
Whether the definition line is harmless depends on the paragraph's final layout, which is only known after the paragraph is reflowed.

**Put both directions together and you have a cycle.** In a cluster holding a definition-shaped line and the paragraphs around it, the upper block's meaning depends on the lower block's final bytes (title absorption) while the lower block's safety verdict depends on the upper block's final bytes (the shape on the line above).
A depends on B's answer and B depends on A's.
Processing blocks top-down against already-updated predecessors does not resolve this: a downward sweep fixes the staleness in the trace above but leaves every upward-looking verdict reading bytes that a later block is about to change.
Sweeping repeatedly until nothing moves is fixpoint iteration, which has no termination guarantee here (escape oscillators cycle forever) and cannot repair a render corruption once a single pass mints one.
A verdict is only trustworthy if the bytes it reads cannot move at all, which is what freezing provides.

### The block pass reads labels backtick-blind

CommonMark link labels may contain backticks, and definitions are extracted before code spans exist.
So the inline view and the block view of the same bytes can disagree:

```
[see the `option]: value` form for details, plus more prose here.
```

Inline-wise, `` `option]: value` `` is a code span and the `]:` is
quoted text. Block-wise, `` [see the `option] `` is a legal label
followed by a colon. If a rewrap leaves that shape alone at a
paragraph's start, it reparses as a real definition and the text
vanishes from the render. This is why "ignore bracket shapes inside
code spans" is not a safe rule, even though it sounds obviously right
(issue #37 has the full postmortem, including three distinct ways the
rule fails under fuzzing).

### A split can mint a new block type

Nested list markers and thematic breaks share characters.
A split that lands `**` as the first line of a bullet nested in an ordered item can reparse as `* **`: a thematic break, which ends the list.
Emphasis markers, list bullets, and break runs are all drawn from the same tiny alphabet, and reflow moves them to line starts where the block pass gives them block meanings.

### Escapes must be stable under their own reparse

When emission cannot avoid placing a hazardous shape at a line start, it backslash-escapes it.
But the escaped spelling is itself bytes that the next pass will judge.
Any guard whose verdict differs between the escaped and unescaped spelling of the same construct oscillates: pass one escapes, pass two (seeing different bytes) makes a different decision, and the output never settles.
Raw HTML openers (`<?`, `<!`) have exactly this property, because `\<?` no longer parses as HTML at all, which changes the span geometry the next pass computes.

## Why the verdicts are blunt

The pattern behind all of the above: **a freeze verdict may only depend on bytes that reflow cannot move, and every predicate keyed on a shape must judge all spellings of that shape the same way.** Precise guards fail this test constantly.
A guard that inspects context ("is this bracket really a definition?") reads bytes that some other block's reflow may rewrite.
The historical version of the definition-zone logic grew six interlocking adjacency guards and the fuzzer kept finding a seventh shape.

The blunt alternative: freeze by shape, on raw bytes, over the whole dangerous neighborhood.
Frozen bytes are the only bytes guaranteed to reparse identically.
Decisions are made per block, but the inputs are cluster-scoped (the raw line above, def-shaped lines anywhere in the contiguous run), so the freeze's blast radius approximates the dangerous part of the cluster.

The cost is real but small and measurable: on a 266-file production docset, the definition zone freezes roughly ten lines, concentrated in documents that quote `label:` syntax inside code spans.
The design trade is coverage of rare shapes for guaranteed render preservation of common ones.

The MkDocs admonition marker (`!!! note`, `??? warning`, `???+ tip`) is the same lesson learned twice.
An earlier version of the marker regex required a type word after the punctuation and anchored the match at end of line — `^(?:!{3}|\?{3}\+?)[ \t]+[A-Za-z][\w-]*(?:[ \t]+"[^"]*")?[ \t]*$` — on the theory that an ordinary paragraph merely starting with `!!!` shouldn't be misread as a callout.
The end anchor reads a byte reflow moves: a wrap cut that happens to fall right after `!!! ev` leaves that alone on a line, which the anchored regex then matched, even though the source line was `!!! ev BX1201` and never meant to open an admonition (issue #51).
The verdict flipped depending on where the *previous* pass had broken the line — the same class of self-inflicted instability as the definition zone above, just triggered by the wrap loop instead of a join.
The fix is the same blunt move: match the prefix only, unanchored, so the verdict depends solely on the three or four bytes reflow never relocates.
The measured cost is a paragraph starting `!!!` or `???` no longer reflowing under the mkdocs dialect, however the rest of the line reads — narrower coverage of a rare paragraph opener, traded for a guaranteed fixpoint.
The type-word requirement also turned out to reject real syntax: Material for MkDocs's inline modifiers (`!!! note inline end "Title"`) never matched it, so those admonitions' bodies were silently skipped from reflow entirely; the blunt prefix match recognizes them as a side effect of no longer trying to parse the rest of the line at all.

## Dialects multiply the grammar

Everything above assumed one grammar.
There are two dialects (GFM and MkDocs), and a dialect changes both layers at once.

**Span geometry shifts.** GFM linkify turns bare URLs into links, and a backtick inside a bare URL is destination content, not a delimiter.
The same bytes have different span boundaries with linkify on or off, which means different no-break regions and different legal splits (issue #33 traced a family of frozen lines to exactly this).

**Block triggers shift.** MkDocs admonitions make `!!! note` a marker line whose body must keep a 4-space indent.
In GFM those are ordinary paragraph bytes.
A line shape that is inert in one dialect is load- bearing structure in the other.

**Extensions redraw category lines.** With footnotes enabled, `[^x]:` is a footnote definition (a reflowable body); without them it is an ordinary link reference definition (frozen metadata).
The classification of a single line flips with a parser flag.

The consequence for correctness work: every hazard analysis is per- dialect, and fuzzing coverage must exercise each dialect separately, because a soak that only ever parses GFM proves nothing about MkDocs inputs (issue #26 tracks this gap).

## What authors can do about a frozen paragraph

The freezes are shape-based, so authors can remove the shape.
In rough order of preference:

- **Move Markdown-syntax-looking literals into fenced code blocks.** A
  quoted error message or config fragment containing `label]:`, `<?`,
  or bracket shapes is the most common freeze trigger. In a fenced
  block it is a separate, never-reflowed block and the surrounding
  prose becomes ordinary. For error output this is usually better doc
  style anyway.
- **Blank-separate the neighbor, if the render change is acceptable.**
  A blank line detaches an adjacent block from the dangerous one and
  its freeze. Inside a list this makes the list loose, which is
  visible; between top-level blocks it is usually free.
- **Hand-format the frozen paragraph once.** A freeze is byte-for-byte
  passthrough, not damage. A paragraph formatted by hand stays exactly
  as written, forever. For prose that genuinely documents definition
  syntax, this is the correct end state.

What does not work: backslash-escaping the bracket (the zone deliberately judges escaped and unescaped spellings alike, because reflow's own emission escapes must stay frozen), and escaping inside a code span (backslashes are literal there).

## Takeaways

- The safe unit of reasoning is the cluster, not the block.
  Any proposed fix that judges a block in isolation is wrong or lucky.
- "Just look inside the code span" and "just add blank lines" are the two most tempting fixes, and both are unsound for reasons that only show up under adversarial input.
  The fuzzer, not code review, is the arbiter.
- When a paragraph does not reflow, the first question is which freeze fired and what it protects.
  The answer is usually in design.md's zone section or in the fuzz seed referenced by the guard's comment.
- Blunt rules with measured, small coverage cost beat precise rules with unbounded verification cost.
  This trade is deliberate and has survived contact with hundreds of millions of fuzz executions.
