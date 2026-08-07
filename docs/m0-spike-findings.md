# M0 spike: goldmark vs MDX/Docusaurus/Hugo dialects

*2026-08-07.*

Answers the [design.md](design.md) skip-list question: where do MDX/Docusaurus/Hugo constructs land in goldmark's AST, and does any of them land as a `Paragraph` node (reflow's corruption risk) instead of `HTMLBlock`/other pass-through kinds?

## Method

Probe program: `goldmark v1.8.5` + `extension.GFM` + `extension.Footnote`,
parsing 10 synthetic sample files and dumping every node's `Kind()`, byte
range, and source text (script and fixtures not committed — scratch spike
code per the milestone). A second probe added `goldmark-meta v1.1.0` to test
whether an off-the-shelf extension fixes YAML front matter. Node dumps below
are excerpted directly from probe output; byte ranges are into the sample
file, not this doc.

## Summary

| Construct | Node kind | Corruption risk | Notes |
|---|---|---|---|
| MDX `import`/`export` statements | **Paragraph** | **Yes** | Indistinguishable from prose text |
| MDX JSX block, tag closes on its opening line, blank-line delimited | HTMLBlock | No | Interior content is separate `Paragraph` nodes |
| MDX self-closing `<X />` | HTMLBlock | No | |
| MDX JSX tag with attributes spanning multiple lines | **Paragraph** (garbled) | **Yes** | Also corrupts block *structure*: a lone `>` closing the tag is misread as an empty `Blockquote` |
| MDX inline JSX / component in prose | RawHTML (inline) + Text | No | Needs no-break-span protection, not skip |
| MDX block-level `{expr}` on its own line | **Paragraph** | **Yes** | |
| MDX inline `{expr}` | plain `Text`, no marker node | Not corruption, but invisible to the AST | mdreflow must text-scan for it itself |
| Docusaurus `:::` fence, blank line around body | Paragraph (fence alone) + Paragraph (body) | Low | Fence line is its own single-line `Paragraph`, cleanly separated |
| Docusaurus `:::` fence, **no** blank line around body | **Paragraph** (fence+body+closing fence merged, sometimes trailing prose too) | **Yes, severe** | |
| Nested admonitions | Same per-fence-line behavior, no nesting awareness | as above | |
| YAML front matter, plain goldmark+GFM | ThematicBreak + Paragraph + List soup | **Yes, severe** | |
| YAML front matter, with `goldmark-meta` extension | Removed from AST, parsed to metadata map | **No — solved** | |
| TOML front matter (`+++`) | **Paragraph** (entire block, incl. delimiters) | **Yes, severe** | `goldmark-meta` does not handle TOML |
| Hugo shortcode block `{{< … >}}` alone on a line | **Paragraph** | **Yes** | |
| Hugo paired shortcode `{{% %}} … {{% /%}}`, no blank lines | **Paragraph** (all lines merged) | **Yes** | |
| Hugo shortcode inline | plain `Text` | Not corruption, but invisible to the AST | Same as inline `{expr}` |
| GFM inline math `$…$` | plain `Text` | Not corruption, but invisible to the AST | No math extension enabled; would still be plain Text even with one, for skip-list purposes |
| GFM math block `$$…$$` | **Paragraph** (fences+body merged) | **Yes, severe** | |
| GitHub alert `> [!NOTE]` | **Paragraph** inside Blockquote, marker line merged with body | **Yes** | Contradicts design.md's assumption that the marker line stays intact |
| GFM tables | Table/TableRow/TableCell | No | Cell content is `Text` directly, never wrapped in `Paragraph` |
| Footnote definition body | Paragraph | N/A | Correctly *should* reflow — not a corruption case |
| `<3 hearts…` (paragraph starting with `<`, not valid HTML) | Paragraph, plain text | No | goldmark's tag grammar correctly rejects it |

## Evidence

### MDX import/export — lands as Paragraph

```
#2 Paragraph {
  line[0:32] = "import Tabs from '@theme/Tabs';\n"
  line[32:69] = "import TabItem from '@theme/TabItem';"
}
```

No distinguishing marker; a naive sentence-per-line pass would treat `import Tabs from '@theme/Tabs';` as a sentence and could re-break it at the comma-free "boundary," or merge it with the next import line.

### MDX JSX block — clean when the tag closes on one line

```
#28 HTMLBlock {
  line[115:122] = "<Tabs>\n"
  line[122:154] = "<TabItem value=\"go\" label=\"Go\">\n"
}
#29 Paragraph {
  line[155:232] = "Some indented prose inside the tabs component describing the Go install path."
}
```

goldmark's CommonMark HTML-block condition 7 (a line that is *only* a
complete open/close tag) fires correctly here and at `</TabItem>`,
`<TabItem value="js"...>`, `</TabItem>`, `</Tabs>` — all four become
`HTMLBlock`. Interior prose paragraphs are correctly delimited.

### MDX JSX with multi-line attributes — corrupts both content and structure

Sample:
```
<CustomCard
  title="Example"
  variant="primary"
  onClick={() => {
    doSomething();
  }}
>

Prose inside the card component...
```

```
#67 Paragraph {
  line[0:12] = "<CustomCard\n"
  line[14:30] = "title=\"Example\"\n"
  line[32:50] = "variant=\"primary\"\n"
  line[52:69] = "onClick={() => {\n"
  line[73:88] = "doSomething();\n"
  line[90:92] = "}}"
}
#76 Blockquote { }         <-- the lone ">" line
#77 Paragraph { ... prose ... }
```

Because the opening tag doesn't close (`>`) on its own line, HTML-block
condition 7 never matches, so goldmark falls through to ordinary paragraph
parsing: every attribute line becomes prose text ("onClick={() =>" / " {" as
separate `Text` nodes — a reflow pass would rejoin and re-wrap these as if
they were a sentence). Separately, the lone `>` that closes the tag is valid
CommonMark for an empty blockquote, so it's consumed as `Blockquote`, splitting
the JSX region across three unrelated nodes. This is the one case in the
spike where AST post-processing can't recover the original intent — the
information needed ("this `>` is JSX punctuation, not a quote marker") is
already lost by the time goldmark hands back the tree.

### Docusaurus `:::` — fence-body separation depends entirely on blank lines

With blank lines around the body (`:::note` / blank / prose / blank / `:::`):

```
#89 Paragraph { line[0:7] = ":::note" }
#91 Paragraph { line[9:84]/line[84:151] = "This is a note body..." }
#96 Paragraph { line[153:156] = ":::" }
```

Three clean, separate `Paragraph` nodes — the fence lines are trivially detectable by content (`^:::`) and each is its own whole node.

Without blank lines (`:::tip[Custom Title]` directly against body directly
against closing `:::`):

```
#98 Paragraph {
  line[158:179] = ":::tip[Custom Title]\n"
  line[179:253] = "Tip content directly against the fence with no blank line separating them\n"
  line[253:321] = "here, to see whether the fence line merges into the paragraph text.\n"
  line[321:324] = ":::"
}
```

One `Paragraph` node, four `Lines()` segments, fence markers and prose fused.
Worst case observed (`10-edge-cases.md`, fence immediately touching prose on
*both* sides with zero blank lines):

```
#261 Paragraph {
  line[323:331] = ":::note\n"
  line[331:408] = "No blank line directive immediately followed by prose..."
  line[408:412] = ":::\n"
  line[412:487] = "Prose immediately after a closing fence..."
}
```
Here even the prose *after* the closing fence gets swallowed into the same node as the fence and the directive's interior prose — ordinary CommonMark lazy-paragraph-continuation, since nothing about `:::` is block-structural to goldmark.

### Front matter

YAML, plain goldmark+GFM — decomposes into unrelated blocks:

```
#125 ThematicBreak { }
#126 Paragraph { "title: Sample Doc" / "description: A synthetic..." / "tags:" }
#132 List { ListItem > TextBlock "one", ListItem > TextBlock "two" }
#139 ThematicBreak { }
```

`description: A synthetic sample used for the M0 spike probe.` is plain paragraph text — a reflow pass would sentence-split or rewrap a YAML value.

YAML with `goldmark-meta` (`github.com/yuin/goldmark-meta v1.1.0`):

```
[Document]
  [Paragraph] line[110:188]... line[188:249]...   <- only the post-front-matter prose
Meta: map[string]interface {}{"description":"...", "tags":[...], "title":"Sample Doc"}
```

The front matter block is fully removed from the AST and parsed separately — no corruption risk, confirmed by direct test.

TOML (`+++`) — `goldmark-meta` does not trigger on it (its `Trigger()` is
`-`, and the linked YAML unmarshal silently no-ops on TOML content):

```
#146 Paragraph {
  line[0:4] = "+++\n"
  line[4:25] = "title = \"Sample Doc\"\n"
  line[25:89] = "description = \"A synthetic sample used for the M0 spike probe.\"\n"
  line[89:111] = "tags = [\"one\", \"two\"]\n"
  line[111:114] = "+++"
}
```
Confirmed with the metaprobe run: `Meta: map[string]interface {}(nil)` for the TOML file — the whole block is one giant reflow-eligible `Paragraph`.

### Hugo shortcodes

Block-level, own line:
```
#167 Paragraph { line[97:151] = "{{< figure src=\"image.png\" alt=\"An example figure\" >}}" }
```

Paired, no blank lines — same lazy-continuation merge as `:::`:
```
#173 Paragraph {
  line[232:252] = "{{% notice note %}}\n"
  line[252:323] = "Percent-delimited shortcode body content...\n"
  line[323:357] = "spanning more than one line here.\n"
  line[357:372] = "{{% /notice %}}"
}
```

Inline, mid-sentence — plain `Text`, no marker: `{{< param foo >}}` shows up as ordinary text inside `#183 Text { "...like {{< param foo >}} right here" }`.

### GFM math block — merges fence and content

```
#196 Paragraph {
  line[101:104] = "$$\n"
  line[104:113] = "E = mc^2\n"
  line[113:136] = "\\quad \\text{and} \\quad\n"
  line[136:143] = "F = ma\n"
  line[143:145] = "$$"
}
```
No math extension is enabled in `extension.GFM`; `$$` has zero special meaning to goldmark core, so this is ordinary lazy-continuation paragraph merging, identical mechanism to the `:::`/shortcode cases. Inline `$E = mc^2$` is likewise plain `Text` with no marker.

### GitHub alert — contradicts design.md's assumption

design.md's skip-list table says: *"line stays intact; rest of quote reflows."* The probe shows the marker line and body are **not** separated without a dedicated alert extension — goldmark (GFM extension set) has no concept of `[!NOTE]`:

```
#205 Blockquote {
  #206 Paragraph {
    line[149:157] = "[!NOTE]\n"
    line[159:231] = "This is an alert body...\n"
    line[233:307] = "that should probably reflow..."
  }
}
```
One `Paragraph`, three `Lines()`.
This needs the same line-boundary handling as the no-blank-line `:::` case, not the clean node-level skip design.md implies.

### Tables and footnotes — safe

Table cell content lands directly as `Text` under `TableCell`, never wrapped in `Paragraph`:
```
#216 TableCell { line[311:319] = "Column A" #217 Text{"Column"} #218 Text{" A"} }
```
Footnote definition bodies are `Paragraph` — correctly so, since that prose *should* reflow; not a corruption case, just confirms the definition text is reachable like any other paragraph.

## Recommendation

**A full raw-byte, pre-goldmark-parse pre-pass is not needed for the general
case.** Every corruption case except one is fully recoverable by working with
goldmark's AST — extending the "block map derivation" step (already in
design.md's architecture) with **content-pattern rules**, not by scanning
source text before goldmark ever sees it:

1. **Whole-node text-sniffing.** For `Paragraph` nodes whose entire joined
   text matches a known marker regex (`^:::`, `^\{\{[<%]`, `^\{.*\}$`
   block-level `{expr}`, a lone `^\+\+\+$`/`^---$` line), skip the whole
   node. Covers: bare `:::` fence lines, bare Hugo block shortcodes, bare
   MDX block `{expr}`, and — combined with rule 2 — TOML front matter.
2. **Line-level boundaries inside a Paragraph.** `Paragraph.Lines()` already
   exposes per-source-line segments. Where a fence/marker line has merged
   into the same `Paragraph` node as real prose (`:::` with no blank line,
   GitHub alert marker, Hugo paired shortcode, math block), treat any
   `Lines()` entry matching the marker regex as an immovable boundary: never
   join across it, emit it byte-for-byte, and reflow only the runs of lines
   on either side that are genuine prose. This handles every "fence fused
   with body" case found, including the worst case (`10-edge-cases.md`,
   prose-fence-prose all one node).
3. **TOML front matter specifically** needs its own recognizer — no existing
   goldmark extension parses it (confirmed: `goldmark-meta` no-ops on it).
   Simplest fix: adopt `goldmark-meta` (small, MIT, already does exactly this
   for YAML) for YAML, and add an equivalent trigger-on-`+++` skip rule (via
   rule 1, since the whole node's first line is always `+++` in the sample
   set) or a small custom `parser.BlockParser` modeled on it if pure
   text-sniffing proves fragile in practice.
4. **Inline no-break spans are invisible to goldmark entirely** — `{expr}`, Hugo inline shortcodes, and inline math never get a marker node; they're plain `Text`.
   This isn't AST-derivable at all and was already anticipated in design.md's "no-break inline spans" section — confirms that component needs its own regex scan over inline `Text` content, independent of goldmark's classification.
5. **The one case needing a true pre-parse workaround**: multi-line JSX
   opening tags whose closing `>` lands alone on its own line. goldmark
   misreads that `>` as blockquote syntax *before* mdreflow gets an AST, so
   post-parse content rules can't reconstruct the original JSX region — the
   information is already gone. Recommend either (a) a narrow raw-line
   pre-check that recognizes `<Identifier` opening with no `>` before EOL,
   followed later by a bare `>` line, and fences that region off before
   goldmark parses, or (b) documenting this as a known M1 limitation and
   revisiting only if it's observed in real MDX content (Docusaurus/MDX
   authors generally close simple tags on one line; multi-line attribute
   lists are less common than the blank-line-delimited JSX block form, which
   parses cleanly).

Net: design.md's fallback ("a line-scanning pre-pass that fences off
JSX/shortcode regions before goldmark sees the text") is not needed as a
general mechanism — an AST + content-regex layer, still built from goldmark's
read-only parse, covers essentially everything. Reserve an actual raw pre-pass
for the single structural-corruption edge case (multi-line unclosed JSX tags),
and only if it turns out to matter in practice.
