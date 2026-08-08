package segment

import "regexp"

// NoBreakSpans returns byte ranges within text that a sentence break must
// never land inside: inline code spans, links and images (including
// one-level-nested-bracket link/image text), autolinks, footnote
// references, inline math, inline Hugo shortcodes, inline MDX/JSX {expr}
// spans, and inline HTML/JSX tags. It is a deliberately simple, text-based
// approximation of CommonMark's (and these dialects') inline grammar — a
// span longer than any configured width limit simply overflows; break
// points just never land inside one.
//
// The reflow pipeline uses this to filter a Segmenter's candidate breaks,
// independent of which Segmenter produced them.
func NoBreakSpans(text string) []Span {
	var out []Span
	out = append(out, codeSpans(text)...)
	out = append(out, bracketedSpans(text)...)
	out = append(out, otherSpans(text)...)
	return out
}

// CodeSpans is codeSpans, exported for package reflow: it needs to know a
// paragraph's real (matched-open-and-close) code span ranges, across its
// raw source lines joined by "\n", to tell whether a given line boundary
// falls inside one — the naive "odd backtick count so far" parity a purely
// per-line scan can afford is not sufficient, since an opening run with no
// later matching close is not a span at all (see joinClusterLines and
// detectHardBreak in package reflow for what this fixes).
func CodeSpans(text string) []Span {
	return codeSpans(text)
}

// codeSpans finds CommonMark-style backtick code spans: a run of one or
// more backticks, then the shortest run of backticks of the same length
// later in the text. An opening run with no matching close is not a code
// span and is skipped, matching CommonMark.
func codeSpans(text string) []Span {
	var out []Span
	i := 0
	for i < len(text) {
		if text[i] != '`' {
			i++
			continue
		}
		if precededByOddBackslashes(text, i) {
			// A backslash-escaped backtick renders as a literal backtick
			// and cannot open a code span — confirmed against goldmark,
			// not assumed (`\`a\`` renders as literal "`a`", no <code>).
			// Only the opening backtick of a run is checked: once inside
			// a genuine span, closing-search does not re-check this
			// (backslash escapes do not work inside code spans at all,
			// per CommonMark), so this exclusion applies once, here, at
			// the point a new run is considered as a candidate opener —
			// not inside the closing-search loop below. This matters for
			// package reflow's own escapeBlockInterrupt, which
			// backslash-escapes every backtick of a fenced-code-opener
			// run to defeat it: without this exclusion, the now-escaped
			// backticks could still spuriously pair up with unrelated
			// backticks elsewhere in the text as this function's own
			// false-positive code span — found by FuzzFormat via a
			// resulting idempotency break on "```! 0`0".
			i++
			continue
		}
		start := i
		j := i
		for j < len(text) && text[j] == '`' {
			j++
		}
		runLen := j - start

		k := j
		closed := -1
		for k < len(text) {
			if text[k] != '`' {
				k++
				continue
			}
			m := k
			for m < len(text) && text[m] == '`' {
				m++
			}
			if m-k == runLen {
				closed = m
				break
			}
			k = m
		}
		if closed >= 0 {
			out = append(out, Span{Start: start, End: closed})
			i = closed
		} else {
			i = j // unmatched opening run; not a code span
		}
	}
	return out
}

// bracketedSpans finds links and images — "[text](dest)", "[text][ref]",
// and shortcut reference "[text]" — by manually matching brackets and
// parens.
//
// Matching is nearest-opener first (a stack of "[" / "![" positions, the
// same order CommonMark's own bracket-delimiter algorithm resolves in),
// not first-opener-outward depth counting. That distinction matters for
// two adjacent unmatched openers with no text between them, "[[](url)":
// depth counting would greedily pair the *first* "[" with the "]" that
// balances its depth back to zero, landing on the wrong "]" — the one
// immediately before "(url)" belongs to the *second*, inner "[", per
// CommonMark's own "nearest unmatched opener" rule, and the first "["
// never resolves into anything (a link cannot contain a link, so it stays
// literal text). Depth counting silently claimed the wrong span [text
// "[[](url)" whole] and left the real destination just past it
// unprotected. Nearest-opener matching pops the inner "[" first, forms
// the real link "[](url)" against it, and leaves the outer "[" on the
// stack unresolved (matching goldmark's own rendering) — found by
// FuzzFormat on "2[[](()]\")" (checked in as
// testdata/fuzz/FuzzFormat/643a723f2e9d178a), where the un-protected
// quote inside the real link's destination got curled by the typography
// pass, corrupting the percent-encoded destination on render.
//
// One level of nested "[...]" inside link/image text (e.g. a footnote
// marker embedded in link text) still ends up covered: the inner bracket
// resolves to its own (harmless, redundant — overlapping no-break spans
// are fine) span first, and the outer link's span, resolved afterward
// against its own "]", still spans the whole thing since nothing consumed
// the outer "]" out from under it.
//
// An optional leading "!" (image) is included in the span. An opener left
// on the stack with no "]" ever resolving it is left unprotected, matching
// CommonMark (it is not a link).
func bracketedSpans(text string) []Span {
	var out []Span
	var stack []int // byte offsets of unresolved "[" / "![" openers
	i := 0
	for i < len(text) {
		switch {
		case text[i] == '\\' && i+1 < len(text):
			i += 2
		case text[i] == '!' && i+1 < len(text) && text[i+1] == '[':
			stack = append(stack, i)
			i += 2
		case text[i] == '[':
			stack = append(stack, i)
			i++
		case text[i] == ']':
			if len(stack) == 0 {
				i++
				continue
			}
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			closeBracket := i + 1
			end := closeBracket

			if closeBracket < len(text) && text[closeBracket] == '(' {
				// Two independent attempts, the larger extent wins — see
				// naiveParenBalance's doc comment for why one scan is
				// not enough on its own.
				if k, ok := scanLinkDestTitle(text, closeBracket); ok && k > end {
					end = k
				}
				if k, ok := naiveParenBalance(text, closeBracket); ok && k > end {
					end = k
				}
			}
			// A "[ref]" reference label immediately following this "]" —
			// "[text][ref]" full-reference-style — is deliberately *not*
			// folded into this same span the way a "(dest)" inline
			// destination is above. Whether "[text][ref]" actually
			// resolves as one link depends on whether "ref" matches a
			// link reference definition elsewhere in the document, which
			// this function has no visibility into (and, per its own doc
			// comment, doesn't need: a bare "[text]" is already
			// protected as a possible shortcut reference without
			// checking a definition exists). Assuming it always resolves
			// was actively wrong: found by FuzzFormat on "[][](\")1000",
			// where the first "[]" does *not* resolve as a link at all
			// (no such reference is defined), so CommonMark's own
			// nearest-opener algorithm retries from the second "[" —
			// which *does* form a real inline link, "[](\")" — but the
			// old code had already folded both bracket pairs into one
			// "[][]" reference-style span here, leaving that real link's
			// destination (containing the quote the failing input curled)
			// outside of it. Leaving end at closeBracket instead — not
			// reaching into the following "[ref]" — protects only
			// "[text]" here; the next loop iteration then sees the
			// following "[" fresh and protects "[ref]" (or a real link,
			// if one starts there) as its own, independent span. For a
			// genuine "[text][ref]", the two adjacent spans still union
			// to the same coverage a single combined span would have
			// given; nothing is lost for the case this was meant to
			// protect, and the wrong-match hazard above is closed.
			out = append(out, Span{Start: start, End: end})
			i = end
		default:
			i++
		}
	}
	return out
}

// scanLinkDestTitle reports whether text has a CommonMark-shaped inline
// link/image destination, with an optional title, starting at byte offset
// open (where text[open] == '('), returning the byte offset just past its
// closing ')' on success.
//
// This replaced a naive "count '(' and ')' until balanced" scan, which
// does not know a title is a quoted, opaque unit: a ')' *inside* a
// quoted title ends the destination's own paren-balance early, since
// nothing about depth-counting knows to stop looking at quote
// characters. Found by FuzzFormat on `[](0 ")")0` — a link with
// destination "0" and title `")"` (a title whose own text is a single,
// unescaped close-paren) — where the naive scan's depth hit zero at the
// ')' *inside* the title, ending the span there and leaving the title's
// own closing '"' outside it, unprotected and curled by the typography
// pass, which changed the link's title text on render.
//
// The grammar mirrored here (CommonMark's link destination and title):
// an optional "<...>"-bracketed destination or a bare run of non-
// whitespace characters balancing its own unquoted parens, then
// optional whitespace, then an optional title opened and closed by a
// matching '"', a single quote, or '(' / ')' pair (whose own interior —
// including any ')' — is not otherwise inspected), then optional
// whitespace, then
// the closing ')'.
func scanLinkDestTitle(text string, open int) (int, bool) {
	i := open + 1
	for i < len(text) && isLinkSpace(text[i]) {
		i++
	}
	if i < len(text) && text[i] == '<' {
		i++
		for i < len(text) && text[i] != '>' && text[i] != '\n' {
			if text[i] == '\\' && i+1 < len(text) {
				i++
			}
			i++
		}
		if i >= len(text) || text[i] != '>' {
			return 0, false
		}
		i++
	} else {
		depth := 0
		for i < len(text) {
			c := text[i]
			if c == '\\' && i+1 < len(text) {
				i += 2
				continue
			}
			if isLinkSpace(c) {
				break
			}
			if c == '(' {
				depth++
			} else if c == ')' {
				if depth == 0 {
					break
				}
				depth--
			}
			i++
		}
	}

	wsStart := i
	for i < len(text) && isLinkSpace(text[i]) {
		i++
	}
	if i > wsStart && i < len(text) && (text[i] == '"' || text[i] == '\'' || text[i] == '(') {
		titleOpen, titleClose := text[i], text[i]
		if titleOpen == '(' {
			titleClose = ')'
		}
		i++
		for i < len(text) && text[i] != titleClose {
			if text[i] == '\\' && i+1 < len(text) {
				i++
			}
			i++
		}
		if i >= len(text) {
			return 0, false
		}
		i++
		for i < len(text) && isLinkSpace(text[i]) {
			i++
		}
	}

	if i < len(text) && text[i] == ')' {
		return i + 1, true
	}
	return 0, false
}

// isLinkSpace reports whether c is whitespace within a link destination
// or title's own grammar — unlike htmlTagSpans's isHTMLTagSpace, this
// deliberately includes '\n': a link's destination and title are part of
// mdreflow's normal paragraph prose, which reflow's own line-joining
// (and, upstream of that, a source paragraph's own soft line breaks) can
// legitimately place a newline inside, so excluding it here would wrongly
// stop treating the rest of a perfectly ordinary multi-line link as
// protected.
func isLinkSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}

// naiveParenBalance reports whether text has a run of balanced
// parentheses starting at byte offset open (where text[open] == '('),
// returning the byte offset just past the matching closing ')'.
//
// This is bracketedSpans's other, independent attempt at a link
// destination's extent, kept alongside scanLinkDestTitle (whichever
// finds the *larger* span wins — see bracketedSpans's call site) rather
// than replaced by it, because the two fail in complementary directions:
// scanLinkDestTitle's grammar-aware destination scan stops at the first
// byte it treats as whitespace, which is narrower than what goldmark
// itself actually tolerates unescaped inside a bare destination — found
// by FuzzFormat on `[]([]"11\v170x0)`, a destination containing a raw
// vertical tab (0x0B), which goldmark still accepts as destination
// content (rendering the whole thing, verbatim, as the href) but which
// scanLinkDestTitle reads as a whitespace break, tries and fails to find
// a title after, and gives up empty-handed. Simple paren-depth counting,
// with no opinion at all about which bytes are "whitespace" and no title
// grammar to fail out of, still gets this shape right (it only fails on
// the opposite hazard: a ')' inside a quoted title, which
// scanLinkDestTitle exists to handle). Between the two, checking both
// and keeping the larger extent is the same "protect more, not less"
// trade this file makes elsewhere, and closes both hazards without
// either scan needing to become a fully accurate implementation of
// goldmark's own destination parser.
func naiveParenBalance(text string, open int) (int, bool) {
	pd := 1
	k := open + 1
	for k < len(text) && pd > 0 {
		switch text[k] {
		case '(':
			pd++
		case ')':
			pd--
		}
		k++
	}
	if pd == 0 {
		return k, true
	}
	return 0, false
}

// Autolinks, footnote references, inline math, inline Hugo shortcodes,
// inline MDX/JSX {expr} spans, and inline HTML/JSX tags. Each is a
// text-based approximation, not a full grammar; see NoBreakSpans's doc
// comment.
var (
	autolinkRE = regexp.MustCompile(`<[A-Za-z][A-Za-z0-9+.-]*:[^\s<>]*>|<[^\s<>@]+@[^\s<>]+>`)

	// footnoteRefRE matches a footnote reference like "[^1]". bracketedSpans
	// already protects these as a degenerate shortcut-reference-style
	// bracket span; this rule is kept as an explicit, independently
	// correct fallback.
	footnoteRefRE = regexp.MustCompile(`\[\^[^\]\n]+\]`)

	// inlineMathRE matches single-dollar inline math, "$...$". It does not
	// attempt to distinguish from a "$$" display-math delimiter; display
	// math is block-level and handled by the skip-list, not this scanner.
	inlineMathRE = regexp.MustCompile(`\$[^$\n]+\$`)

	// hugoShortcodeRE matches an inline Hugo shortcode, "{{< ... >}}" or
	// "{{% ... %}}".
	hugoShortcodeRE = regexp.MustCompile(`\{\{[<%].*?[%>]\}\}`)

	// curlyExprRE matches an inline MDX/JSX "{expr}" span. It also matches
	// (harmlessly, redundantly) the interior of a Hugo shortcode span,
	// since both use curly braces; overlapping no-break spans are fine, a
	// break is blocked if it lands in any of them.
	curlyExprRE = regexp.MustCompile(`\{[^{}\n]+\}`)
)

// precededByOddBackslashes reports whether text[pos] is immediately
// preceded by an odd number of backslash characters — i.e. whether it is
// itself backslash-escaped.
func precededByOddBackslashes(text string, pos int) bool {
	n := 0
	for n < pos && text[pos-1-n] == '\\' {
		n++
	}
	return n%2 == 1
}

func otherSpans(text string) []Span {
	var out []Span
	for _, re := range [...]*regexp.Regexp{
		autolinkRE,
		footnoteRefRE,
		inlineMathRE,
		hugoShortcodeRE,
		curlyExprRE,
	} {
		for _, m := range re.FindAllStringIndex(text, -1) {
			out = append(out, Span{Start: m[0], End: m[1]})
		}
	}
	out = append(out, htmlTagSpans(text)...)
	out = append(out, htmlTagOpenerSpans(text)...)
	return out
}

// htmlTagOpenerRE matches the start of an inline HTML/JSX tag: "<",
// optionally "/", then a letter — CommonMark's own tag-name-start rule,
// deliberately not requiring the rest of the tag to be well-formed (see
// htmlTagOpenerSpans's doc comment on why).
var htmlTagOpenerRE = regexp.MustCompile(`</?[A-Za-z]`)

// htmlTagOpenerSpans returns, for every same-line inline-HTML tag opener
// ("<" or "</" followed by a letter), the byte range from that opener
// through the next ">" on the same line (or through the line's end, if
// none) — regardless of whether that stretch actually satisfies
// htmlTagSpans's own, precise CommonMark tag grammar.
//
// This broader, opener-to-next-">" span exists specifically to keep a
// *break point* out of a "maybe a tag" region even when htmlTagSpans
// itself declines to call it one, which happens whenever the tag grammar
// doesn't quite validate (an attribute with no value, say). Without it,
// NoBreakSpans lets a break land in the middle of such a stretch — which
// can flip the very question of whether it *is* a tag from one reflow
// pass to the next: found by FuzzFormat on `0y70A><A0 A= >00` (ModeWrap,
// width 14), where a width-based break landed between "A=" and " >00",
// starting a fresh line with ">00" — CommonMark reads a bare leading ">"
// as a blockquote marker, so escapeBlockInterrupt backslash-escaped it.
// That backslash is now part of the source on the *next* format pass,
// shifting where the same width computation lands its break — this time
// nowhere at all, since the extra byte changes the fit — so the second
// pass produces different output than the first: an idempotency failure,
// which (unlike a render-preservation gap) design.md does not allow any
// documented exception for. This mirrors package typography's own
// htmlTagOpenerGuardSpans (added for the same underlying reason — an
// ambiguous near-tag region needs one stable, generously wide answer to
// "keep out of this," not two narrower ones that can each land
// differently across passes) but lives here instead so that reflow's own
// break-selection, not just typography's substitution pass, is covered.
func htmlTagOpenerSpans(text string) []Span {
	var out []Span
	lineStart := 0
	for {
		lineEnd := len(text)
		if nl := indexByteFrom(text, lineStart, '\n'); nl >= 0 {
			lineEnd = nl
		}
		line := text[lineStart:lineEnd]
		for _, loc := range htmlTagOpenerRE.FindAllStringIndex(line, -1) {
			start := lineStart + loc[0]
			end := lineEnd
			if gt := indexByteFrom(text, start, '>'); gt >= 0 && gt < lineEnd {
				end = gt + 1
			}
			out = append(out, Span{Start: start, End: end})
		}
		if lineEnd == len(text) {
			break
		}
		lineStart = lineEnd + 1
	}
	return out
}

// indexByteFrom returns the index of the first occurrence of b in
// text[from:], as an absolute offset into text, or -1 if not found.
func indexByteFrom(text string, from int, b byte) int {
	for i := from; i < len(text); i++ {
		if text[i] == b {
			return i
		}
	}
	return -1
}

// htmlTagSpans finds inline HTML/JSX open, close, and self-closing tags,
// e.g. "<Tabs>", "</Tabs>", "<br/>", `<TabItem value="go">`, by walking
// CommonMark's own open/closing-tag grammar byte by byte rather than a
// regex.
//
// This replaced a regex (`</?[A-Za-z][A-Za-z0-9:_-]*(?:\s[^<>\n]*)?/?>`)
// whose attribute-body character class excluded "<", which is legal
// inside a double- or single-quoted attribute value per CommonMark's own
// grammar (only the matching quote character ends the value — "<" has no
// special meaning there). That exclusion made the regex fail to match
// (and so fail to protect) any tag with a "<" inside a quoted attribute
// value at all — found by FuzzFormat on `0<A A000="017<">0` (checked in
// as testdata/fuzz/FuzzFormat/de96be4143586b9a), where the tag's own
// attribute quotes, now completely unprotected, were curled by the
// typography pass, which broke the quoting well enough that goldmark no
// longer recognized the construct as a tag at all and rendered it as
// escaped literal text instead of passing it through raw.
//
// A tag spanning multiple physical lines (whitespace between attributes
// including a newline, which CommonMark's grammar does allow) is
// deliberately not matched, matching the prior regex's behavior — see
// fuzz_test.go's hasMultilineInlineTagCandidate for why that remains an
// accepted, documented gap rather than something this fix expands scope
// to cover.
func htmlTagSpans(text string) []Span {
	var out []Span
	i := 0
	for i < len(text) {
		if text[i] != '<' {
			i++
			continue
		}
		if end, ok := matchHTMLTag(text, i); ok {
			out = append(out, Span{Start: i, End: end})
			i = end
			continue
		}
		i++
	}
	return out
}

// matchHTMLTag reports whether text has a CommonMark-shaped inline HTML
// open, close, or self-closing tag starting at byte offset start (where
// text[start] == '<'), returning the byte offset just past the tag's
// closing '>' on success.
func matchHTMLTag(text string, start int) (int, bool) {
	i := start + 1
	if i >= len(text) {
		return 0, false
	}
	closing := false
	if text[i] == '/' {
		closing = true
		i++
	}
	if i >= len(text) || !isASCIILetter(text[i]) {
		return 0, false
	}
	i++
	for i < len(text) && isTagNameChar(text[i]) {
		i++
	}

	if closing {
		for i < len(text) && isHTMLTagSpace(text[i]) {
			i++
		}
		if i < len(text) && text[i] == '>' {
			return i + 1, true
		}
		return 0, false
	}

	for {
		if i >= len(text) || text[i] == '\n' {
			return 0, false
		}
		wsStart := i
		for i < len(text) && isHTMLTagSpace(text[i]) {
			i++
		}
		hadWS := i > wsStart

		if i < len(text) && text[i] == '/' && i+1 < len(text) && text[i+1] == '>' {
			return i + 2, true
		}
		if i < len(text) && text[i] == '>' {
			return i + 1, true
		}
		if i >= len(text) || text[i] == '\n' || !hadWS {
			return 0, false
		}
		if !isAttrNameStart(text[i]) {
			return 0, false
		}
		i++
		for i < len(text) && isAttrNameChar(text[i]) {
			i++
		}

		save := i
		for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
			i++
		}
		if i < len(text) && text[i] == '=' {
			i++
			for i < len(text) && (text[i] == ' ' || text[i] == '\t') {
				i++
			}
			if i >= len(text) {
				return 0, false
			}
			switch text[i] {
			case '"':
				i++
				for i < len(text) && text[i] != '"' {
					if text[i] == '\n' {
						return 0, false
					}
					i++
				}
				if i >= len(text) {
					return 0, false
				}
				i++
			case '\'':
				i++
				for i < len(text) && text[i] != '\'' {
					if text[i] == '\n' {
						return 0, false
					}
					i++
				}
				if i >= len(text) {
					return 0, false
				}
				i++
			default:
				// CommonMark's own grammar excludes '"' and '\'' from an
				// unquoted attribute value (only whitespace, '=', '<',
				// '>', and '`' end it there), but this scan deliberately
				// keeps going through them anyway: found by FuzzFormat on
				// `s0<A28 X0=0011"182x>0`, whose unquoted value strictly
				// ends at the '"' (making the whole construct not a valid
				// tag at all, since nothing valid follows it directly —
				// goldmark agrees, rendering it as escaped literal text).
				// A strict scan would (correctly, per spec) fail to match
				// here and leave that '"' unprotected — but the '"' was
				// the only thing keeping this from being a tag. Once
				// SmartQuotes curls it to '”', a non-ASCII byte with no
				// special meaning in this grammar, the same bytes *do*
				// satisfy the unquoted-value grammar all the way to the
				// next '>', and goldmark's reparse of the *output* now
				// recognizes a real inline HTML tag where the source had
				// none — a rendered content change, not a typography
				// change. Continuing through '"'/'\'' here trades a
				// little precision (a handful of not-quite-valid-HTML
				// shapes get treated as tags, and so protected, when
				// they are not) for closing that hazard: any '"' or '\''
				// that could tip a near-tag shape into a real tag by
				// disappearing is inside the resulting span instead of
				// outside it, matching this package's standing principle
				// (see typography.go's bareLinkSpans doc comment) of
				// erring toward protecting more rather than less.
				vs := i
				for i < len(text) && text[i] != '\n' && !isHTMLTagSpace(text[i]) &&
					text[i] != '=' &&
					text[i] != '<' && text[i] != '>' && text[i] != '`' {
					i++
				}
				if i == vs {
					return 0, false
				}
			}
		} else {
			i = save
		}
	}
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isTagNameChar reports whether c can appear after a tag name's first
// letter. CommonMark's own grammar allows only ASCII letters, digits, and
// "-"; ":" and "_" are additionally allowed here for JSX/MDX component
// and namespaced tag names (e.g. "<TabItem>", "<svg:rect>"), matching
// what the prior regex accepted.
func isTagNameChar(c byte) bool {
	return isASCIILetter(c) || (c >= '0' && c <= '9') || c == '-' || c == ':' || c == '_'
}

// isAttrNameStart reports whether c can start an HTML attribute name.
func isAttrNameStart(c byte) bool {
	return isASCIILetter(c) || c == '_' || c == ':'
}

// isAttrNameChar reports whether c can appear after an attribute name's
// first character.
func isAttrNameChar(c byte) bool {
	return isAttrNameStart(c) || (c >= '0' && c <= '9') || c == '.' || c == '-'
}

// isHTMLTagSpace reports whether c is whitespace within a tag's own
// grammar, deliberately excluding '\n' — see htmlTagSpans's doc comment
// on why a tag spanning a newline is not matched at all.
func isHTMLTagSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\v' || c == '\f'
}
