package blockmap

import "regexp"

// ruleKind classifies how a dialect skip rule applies to a Paragraph node.
type ruleKind int

const (
	// wholeNodeAny skips the entire paragraph node (never reflowed, never
	// even line-split) if ANY of its raw source lines, trimmed, matches
	// Pattern. Used for constructs where the "body" between two markers is
	// not prose and must not be touched (TOML front matter, math blocks,
	// MDX block {expr}).
	wholeNodeAny ruleKind = iota
	// wholeNodeAll skips the entire paragraph node only if EVERY one of its
	// lines, trimmed, matches Pattern. Used for MDX import/export blocks,
	// which are indistinguishable from prose line-by-line but never mix
	// with real prose within the same fused paragraph.
	wholeNodeAll
	// lineBoundary treats any single matching line as an immovable
	// boundary: it is emitted byte-for-byte and never joined across, but
	// the paragraph's other lines (its "prose runs") reflow normally on
	// either side of it. Used for wrapper syntax whose body is real prose
	// (":::" directives, Hugo paired shortcodes, GitHub alert markers).
	lineBoundary
)

// skipRule is one entry of the M0-recommended skip-list: a content-pattern
// recognizer for one dialect construct, tagged by the dialect it comes
// from. The tag exists so a future --dialect flag (docs/design.md's Future
// directions) can subset these rules instead of requiring new machinery;
// it is not consumed by any code yet.
type skipRule struct {
	Name    string
	Dialect string
	Pattern *regexp.Regexp
	Kind    ruleKind
	// BlockquoteOnly restricts a lineBoundary rule to lines within a
	// Blockquote ancestor (the GitHub alert marker: "> [!NOTE]" is only a
	// marker when it is the first line of a quoted paragraph).
	BlockquoteOnly bool
}

// skipRules is the full M2 skip-list, per docs/m0-spike-findings.md's
// Recommendation section and docs/design.md's skip-list table.
//
// Two constructs design.md marks "skip block" (math, MDX block {expr}, MDX
// import/export, TOML front matter) get wholeNode treatment: their
// "interior" is not prose and must never reflow. The two design.md marks
// as having reflowable interior prose (":::" directives, Hugo shortcodes'
// interior, and — per design.md's own correction in m0-spike-findings.md —
// GitHub alert bodies) get lineBoundary treatment: only the marker line(s)
// are immovable, the surrounding lines in the same fused paragraph reflow.
var skipRules = []skipRule{
	{
		Name:    "docusaurus-directive-fence",
		Dialect: "Docusaurus/remark",
		Pattern: regexp.MustCompile(`^:::`),
		Kind:    lineBoundary,
	},
	{
		Name:    "hugo-shortcode-block",
		Dialect: "Hugo",
		Pattern: regexp.MustCompile(`^\{\{[<%].*[%>]\}\}$`),
		Kind:    lineBoundary,
	},
	{
		Name:           "github-alert-marker",
		Dialect:        "GFM",
		Pattern:        regexp.MustCompile(`^\[!(NOTE|TIP|IMPORTANT|WARNING|CAUTION)\]`),
		Kind:           lineBoundary,
		BlockquoteOnly: true,
	},
	{
		// design.md lists math blocks as "skip block" (unlike ":::", whose
		// row explicitly says "interior prose reflows"): math content is
		// not prose, so the whole fused fence+body+fence paragraph is
		// skipped, not just the "$$" lines. This is a deliberate reading
		// of design.md over m0-spike-findings.md's literal wording (which
		// filed math under the same line-boundary bucket as ":::"); see the
		// M2 report for the full rationale.
		Name:    "math-block-fence",
		Dialect: "GFM/Docusaurus",
		Pattern: regexp.MustCompile(`^\$\$$`),
		Kind:    wholeNodeAny,
	},
	{
		Name:    "mdx-block-expr",
		Dialect: "MDX",
		Pattern: regexp.MustCompile(`^\{[^{}]*\}$`),
		Kind:    wholeNodeAny,
	},
	{
		Name:    "mdx-import-export",
		Dialect: "MDX",
		Pattern: regexp.MustCompile(`^(import|export)\b`),
		Kind:    wholeNodeAll,
	},
	{
		// TOML front matter: goldmark-meta only handles YAML, so "+++"
		// fused blocks land as one giant Paragraph (m0 finding). The
		// opening "+++" line alone is enough to trigger wholeNodeAny.
		Name:    "toml-front-matter",
		Dialect: "Hugo/Docusaurus",
		Pattern: regexp.MustCompile(`^\+\+\+$`),
		Kind:    wholeNodeAny,
	},
}

// wholeNodeSkip reports whether the paragraph whose trimmed lines are
// trimmedLines should be skipped in its entirety (never reflowed, never
// line-split).
func wholeNodeSkip(trimmedLines []string) bool {
	for _, r := range skipRules {
		switch r.Kind {
		case lineBoundary:
			// Not a whole-node rule; see isBoundaryLine.
		case wholeNodeAny:
			for _, l := range trimmedLines {
				if r.Pattern.MatchString(l) {
					return true
				}
			}
		case wholeNodeAll:
			if len(trimmedLines) == 0 {
				continue
			}
			all := true
			for _, l := range trimmedLines {
				if l == "" || !r.Pattern.MatchString(l) {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
	}
	return false
}

// isBoundaryLine reports whether a single trimmed line, in a paragraph
// nested inBlockquote or not, matches a lineBoundary skip rule.
func isBoundaryLine(content string, inBlockquote bool) bool {
	for _, r := range skipRules {
		if r.Kind != lineBoundary {
			continue
		}
		if r.BlockquoteOnly && !inBlockquote {
			continue
		}
		if r.Pattern.MatchString(content) {
			return true
		}
	}
	return false
}
