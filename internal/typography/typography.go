// Package typography implements mdreflow's opt-in, span-level prose
// substitutions: smart quotes and the ellipsis (docs/design.md's
// "Typography" section). It is off by default at every layer above this
// one; nothing here runs unless a caller asks for it.
//
// Typography is the documented exception to mdreflow's render-preservation
// guarantee — changing a straight quote into a curly one is the whole
// point — but it is not an exception to idempotency: Apply never rewrites
// a character it has already produced, so running it twice is the same as
// running it once.
//
// # Scope
//
// Apply substitutes only inside prose the caller hands it, and only
// outside the protected byte ranges the caller supplies (in practice
// segment.NoBreakSpans: inline code, links and their destinations,
// autolinks, footnote references, inline math, inline Hugo shortcodes,
// MDX/JSX {expr} spans, and inline HTML/JSX tags), plus the bare GFM
// linkify URLs and email addresses Apply protects itself (see
// bareLinkSpans). Whole skipped
// constructs — code blocks, front matter, tables, raw HTML blocks,
// dialect-skipped paragraphs — never reach this package at all: package
// reflow only calls it from writeParagraph, which only ever runs on
// blockmap-selected, reflow-eligible paragraph content.
//
// # Known-naive cases, deliberately not solved
//
// The quote heuristic is the well-known SmartyPants-family one: a
// per-character decision from local context, not a matching-pair stack.
// That handles ordinary and nested quoting well and mis-handles a few
// documented shapes, in the same way every tool in this family does:
//
//   - Prime / measurement marks. 6'2" becomes 6’2” — the apostrophe and
//     the double quote are curled like any other closing mark. Detecting
//     feet-and-inches (or arcminutes) is deliberately out of scope.
//   - Elision at a word's start other than a decade. "rock 'n' roll"
//     becomes "rock ‘n’ roll": the first apostrophe sits in an opening
//     context and there is no general way to tell an elided letter from
//     an opening single quote. Decades ('90s) are special-cased because
//     they are common and unambiguous; nothing else is.
//   - Unbalanced quotes. A lone straight quote is curled by its local
//     context alone, so an unclosed opening quote stays an opening quote
//     rather than being reconsidered later.
package typography

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jbeda/mdreflow/internal/segment"
)

// Typography mirrors mdreflow.Typography (an internal package cannot
// import the root package, which imports this one). format.go converts
// between the two; the bit values are kept in lockstep.
type Typography uint

// Typography flags, with the same bit values as mdreflow's.
const (
	// SmartQuotes substitutes straight quotes for curly quotes.
	SmartQuotes Typography = 1 << iota
	// Ellipses substitutes "..." for "…".
	Ellipses
)

// The substitution characters. Byte lengths matter to nothing downstream
// (Apply builds a fresh string rather than splicing), but it is worth
// recording that "..." and "…" are both three bytes, so the ellipsis
// substitution never shifts a byte offset — only the quote substitutions
// (1 byte -> 3) do, which is exactly why protected must be computed on
// the *pre*-substitution text and consulted by original-text position.
const (
	leftDoubleQuote    = "“" // “
	rightDoubleQuote   = "”" // ”
	leftSingleQuote    = "‘" // ‘
	rightSingleQuote   = "’" // ’
	horizontalEllipsis = "…" // …
)

// Apply performs the opts-selected span-level substitutions on text and
// returns the result. Positions covered by protected are copied through
// untouched; protected must be computed on text as given, before any
// substitution (see the doc comment on the substitution constants for
// why).
//
// Apply is a single left-to-right pass. Each decision is made from the
// original text's local context — the immediately preceding byte/rune and
// a bounded lookahead — and written into a fresh output buffer that is
// never re-scanned, so a substituted character can never influence a
// later decision and the pass is idempotent by construction.
//
// Deciding from raw adjacent bytes (rather than trying to see through
// inline markup) is deliberate: it is what makes a quote directly inside
// emphasis delimiters come out right. In **"quoted"** the opening quote's
// preceding byte is '*', which counts as opening context, and the closing
// quote's preceding byte is 'd', which counts as closing context — so
// both resolve correctly without any markup-awareness at all.
func Apply(text string, protected []segment.Span, opts Typography) string {
	if opts == 0 || text == "" {
		return text
	}
	smart := opts&SmartQuotes != 0
	ellipsis := opts&Ellipses != 0
	protected = append(protected[:len(protected):len(protected)], bareLinkSpans(text)...)
	protected = append(protected, htmlTagOpenerGuardSpans(text)...)
	protected = append(protected, linkParenOpenerGuardSpans(text)...)

	var b strings.Builder
	b.Grow(len(text) + len(text)/8)

	for i := 0; i < len(text); {
		c := text[i]
		switch {
		case (c == '"' || c == '\'' || c == '.') && precededByOddBackslashes(text, i):
			// Backslash-escaped: the author wrote "\"" or "\." to get a
			// literal character, and CommonMark only recognizes a
			// backslash escape of an *ASCII punctuation* character.
			// Substituting the escaped character would strand the
			// backslash as visible literal content ("\”" renders as a
			// backslash followed by a curly quote, not as a curly
			// quote), which is a content change, not a typography
			// change. Found by FuzzFormat on "1nX} \\\"0021...", where
			// the escaped straight quote became "\”" and the backslash
			// showed up in the rendered output. Copy the character
			// through untouched instead.
			b.WriteByte(c)
			i++
		case ellipsis && c == '.' && isEllipsisRun(text, i) && !anyProtected(protected, i, i+3):
			b.WriteString(horizontalEllipsis)
			i += 3
		case smart && c == '"' && !inProtected(protected, i):
			if opensQuote(text, i) {
				b.WriteString(leftDoubleQuote)
			} else {
				b.WriteString(rightDoubleQuote)
			}
			i++
		case smart && c == '\'' && !inProtected(protected, i):
			if opensQuote(text, i) && !isDecadeApostrophe(text, i) {
				b.WriteString(leftSingleQuote)
			} else {
				// Closing context, a contraction/possessive
				// ("don't", "the dog's"), or an elided-century decade
				// ('90s) — all of which are the right single quote.
				b.WriteString(rightSingleQuote)
			}
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// isEllipsisRun reports whether exactly three consecutive '.' bytes start
// at i — not two, and not a slice of a longer run. Both edges are checked
// so that "...." (four dots) and any longer run are left entirely alone:
// a run of periods that is not exactly three is not an ellipsis, and
// rewriting part of one would be both wrong and non-idempotent.
func isEllipsisRun(text string, i int) bool {
	if i+3 > len(text) || text[i] != '.' || text[i+1] != '.' || text[i+2] != '.' {
		return false
	}
	if i > 0 && text[i-1] == '.' {
		return false
	}
	return i+3 >= len(text) || text[i+3] != '.'
}

// opensQuote reports whether a quote character at byte position i sits in
// an *opening* context: at the very start of the text, after whitespace,
// or after an opening-punctuation-like character. Anything else — a
// letter, a digit, a closing bracket, sentence punctuation — is a closing
// context.
//
// The "opening-punctuation-like" set deliberately includes the inline
// markup delimiters '*', '_' and '~' and the raw '<' of an HTML tag:
// those are the characters that sit between real whitespace and a quote
// when a quote opens a run of emphasis or bold ( **"quoted"** ), and
// treating them as prose characters would curl such a quote the wrong
// way. It also includes both spellings of an already-opening quote, so a
// nested quote ( "she said 'hi'" ) opens correctly whether or not the
// outer quote has been substituted yet — Apply decides from the original
// text, where the outer quote is still straight.
func opensQuote(text string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:i])
	if unicode.IsSpace(r) {
		return true
	}
	switch r {
	case '(', '[', '{', '<', '-', '–', '—', '/', '*', '_', '~',
		'"', '\'', '“', '‘':
		return true
	}
	return false
}

// isDecadeApostrophe reports whether the apostrophe at byte position i is
// an elided century in a decade abbreviation — "'90s", "'60s", "back in
// the '80s" — which is a right single quote even though it sits in an
// opening context. Without this, the general heuristic would open it
// ("‘90s"), which is the single most visible failure mode of a naive
// smart-quote pass.
//
// The shape recognized is: opening context, then 1-4 digits, optionally
// then an "s"/"S", and then a word boundary (end of text or a non-letter,
// non-digit rune). Callers reach this only after opensQuote already
// returned true, but it re-checks rather than assuming, so it reads
// correctly on its own.
func isDecadeApostrophe(text string, i int) bool {
	if !opensQuote(text, i) {
		return false
	}
	j := i + 1
	digits := 0
	for j < len(text) && text[j] >= '0' && text[j] <= '9' {
		j++
		digits++
		if digits > 4 {
			return false
		}
	}
	if digits == 0 {
		return false
	}
	if j < len(text) && (text[j] == 's' || text[j] == 'S') {
		j++
	}
	if j >= len(text) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(text[j:])
	return !unicode.IsLetter(r) && !unicode.IsDigit(r)
}

// bareLinkRE matches a GFM "linkify" bare link: a scheme-prefixed URL, a
// "www."-prefixed URL, or a bare email address, none of which are
// delimited by any markup at all. Everything up to the next whitespace
// (or "<", where goldmark's own scan stops) is taken as part of the URL;
// trailing punctuation is trimmed off afterwards by bareLinkSpans.
var bareLinkRE = regexp.MustCompile(
	`(?i)(?:[a-z][a-z0-9+.-]*://|www\.)[^\s<]*` +
		"|[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9-]+(?:\\.[A-Za-z0-9-]+)+")

// bareLinkTrailingPunct is the set of trailing characters GFM's linkify
// extension leaves out of a bare URL that has no path component, rather
// than swallowing them into the destination.
const bareLinkTrailingPunct = `"'.,;:!?)]}*_~`

// bareLinkSpans returns the byte ranges of bare GFM linkify URLs and
// email addresses in text — spans the substitutions must not reach into.
//
// This protection is Apply's own, applied on top of whatever the caller
// supplies, because it is specific to what typography does and to nothing
// else in the pipeline. segment.NoBreakSpans covers *delimited* inline
// constructs (code spans, "[text](dest)", "<autolink>", and so on); a
// bare URL has no delimiters, so it is not in that set — and it never
// needed to be, since reflow only ever breaks lines at whitespace and a
// URL contains none. Typography is the first thing in mdreflow that can
// rewrite a byte in the *middle* of a token, which is what makes this
// newly load-bearing: found by FuzzFormat on "http://0.a#020\"0", where
// curling the quote inside a linkified URL truncated the link's
// destination at that byte — a rendered content change, not a typography
// change.
//
// Trailing punctuation is trimmed only from a match with no path,
// query, or fragment component, which is exactly where goldmark's own
// linkify stops swallowing it — confirmed directly against goldmark,
// not derived from GFM's spec text, because the two disagree and
// goldmark is what mdreflow's render-preservation harness compares
// against:
//
//   - "http://a.com\""  links "http://a.com" and leaves the quote as
//     text (the domain scan ends at the quote), so the quote is trimmed
//     here and does get curled.
//   - "http://0.a#\""   links "http://0.a#\"" *including* the quote
//     (once a "#", "?" or "/" starts a path, the scan takes everything
//     up to whitespace), so nothing is trimmed and the quote is left
//     alone. Curling it truncated the link's destination — found by
//     FuzzFormat on exactly this input, after a first version of this
//     function trimmed trailing quotes unconditionally.
//
// Everywhere the two still disagree, this errs toward protecting more
// than goldmark links (e.g. "www.a.com\"x", where goldmark stops at the
// quote): protecting too much only means a substitution is skipped,
// while protecting too little changes what the document renders.
func bareLinkSpans(text string) []segment.Span {
	var out []segment.Span
	for _, m := range bareLinkRE.FindAllStringIndex(text, -1) {
		start, end := m[0], m[1]
		if !hasPathComponent(text[start:end]) {
			for end > start && strings.IndexByte(bareLinkTrailingPunct, text[end-1]) >= 0 {
				end--
			}
		}
		if end > start {
			out = append(out, segment.Span{Start: start, End: end})
		}
	}
	return out
}

// hasPathComponent reports whether a bare link's text carries a path,
// query, or fragment after its authority — the point past which
// goldmark's linkify stops excluding trailing punctuation from the
// destination. The scheme's own "//" is skipped first so it is not
// mistaken for a path.
func hasPathComponent(link string) bool {
	rest := link
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	return strings.ContainsAny(rest, "/?#")
}

// htmlTagOpenerRE matches the start of an inline HTML/JSX tag: "<",
// optionally "/", then a letter — CommonMark's own tag-name-start rule,
// deliberately not requiring the rest of the tag to be well-formed at
// all (see htmlTagOpenerGuardSpans's doc comment on why).
var htmlTagOpenerRE = regexp.MustCompile(`</?[A-Za-z]`)

// htmlTagOpenerGuardSpans is Apply's own extra self-protection, on top of
// whatever the caller's segment.NoBreakSpans already supplies, in the
// same spirit as bareLinkSpans above: it protects every byte from a
// same-line inline-HTML tag opener ("<" or "</" followed by a letter)
// through the next ">" on that line (or through the line's end, if none),
// regardless of whether that stretch currently parses as a *valid*
// CommonMark tag at all.
//
// That "regardless" is the point, and it is deliberately broader than
// segment.NoBreakSpans's own, more precise htmlTagSpans (which only
// protects a stretch it can confirm forms a real tag): a substitution
// inside an *invalid*-looking near-tag stretch can still change whether
// goldmark recognizes it as a tag on reparse, because the very thing
// that makes it invalid can be the ASCII character typography is about
// to remove. Two distinct shapes of this were found by FuzzFormat, both
// SmartQuotes, no width bound needed:
//
//   - `s0<A28 X0=0011"182x>0`: the unquoted attribute value "0011"
//     legitimately ends at the '"' — CommonMark disallows '"' inside an
//     unquoted value — and nothing valid follows directly, so the whole
//     construct is not a tag at all (goldmark escapes it). Once
//     SmartQuotes curls that '"' to '”', a plain non-ASCII byte with no
//     special meaning in this grammar, the unquoted value simply keeps
//     going through it to the next '>' — now a real tag on reparse.
//   - `0<A A="0>`: the attribute value opens a *quoted* value with '"'
//     but never finds a matching close before the line ends, so it is
//     not a tag either (goldmark escapes it). Once that opening '"' is
//     curled away, the same first character after '=' is no longer '"'
//     at all, so attribute-value parsing takes the *unquoted* branch
//     instead — and an unquoted value has no matching-delimiter
//     requirement, so it happily reaches the next '>' and forms a real
//     tag on reparse.
//
// Both are the same underlying hazard from opposite ends: a `"` or `'`
// sitting between a tag opener and its eventual `>` can be exactly the
// byte that keeps a near-tag-shaped run from parsing as a tag, whether
// by disqualifying a value (first shape) or by selecting which
// value-grammar branch applies (second shape). Enumerating every way
// that can happen is an open-ended search; protecting the whole
// candidate stretch broadly, the same trade segment.bracketedSpans and
// bareLinkSpans both already make ("protecting too much only means a
// substitution is skipped, while protecting too little changes what the
// document renders" — see bareLinkSpans's doc comment above), closes the
// class instead of chasing individual shapes of it. The cost is real but
// narrow: a handful of not-quite-valid-HTML-shaped runs keep their
// straight quotes even though they are not, in the end, actual tags.
func htmlTagOpenerGuardSpans(text string) []segment.Span {
	var out []segment.Span
	lineStart := 0
	for {
		lineEnd := len(text)
		if nl := strings.IndexByte(text[lineStart:], '\n'); nl >= 0 {
			lineEnd = lineStart + nl
		}
		line := text[lineStart:lineEnd]
		for _, loc := range htmlTagOpenerRE.FindAllStringIndex(line, -1) {
			start := lineStart + loc[0]
			end := lineEnd
			if gt := strings.IndexByte(text[start:lineEnd], '>'); gt >= 0 {
				end = start + gt + 1
			}
			out = append(out, segment.Span{Start: start, End: end})
		}
		if lineEnd == len(text) {
			break
		}
		lineStart = lineEnd + 1
	}
	return out
}

// linkParenOpenerRE matches a link/image destination's opener: "](".
var linkParenOpenerRE = regexp.MustCompile(`\]\(`)

// linkParenOpenerGuardSpans is Apply's own extra self-protection for link
// and image destinations/titles, in the same spirit as
// htmlTagOpenerGuardSpans just above and bareLinkSpans further up: for
// every same-line "](" opener, it protects every byte from the "("
// through the *last* ")" on that line (not the first, and regardless of
// whether segment.NoBreakSpans's own, more precise bracketedSpans
// currently judges any particular stretch of it to form a valid
// destination or title).
//
// The hazard this closes is a title-quote analog of
// htmlTagOpenerGuardSpans's tag-quote one: a `"` that does not complete a
// *valid* title can still be exactly the byte a title's own closing-quote
// search would have stopped at, so removing it (by curling) does not make
// the construct harmless — it makes the search skip past it to the
// *next* `"` in the text instead, extending a bogus title across
// unrelated, possibly link-shaped content. Found by FuzzFormat on
// `[](0 ")"017[](0")`: the first bracket pair's title opens at the first
// `"`, and the very next `"` should close it — but per CommonMark, a
// title must be followed only by whitespace before the closing `)`, and
// here it is followed by more content ("017[]..."), so the *whole* first
// bracket pair fails to form a link and both `[`/`]` end up literal text
// (confirmed against goldmark: the *second* bracket pair is the only
// real link). segment.bracketedSpans correctly does not protect that
// non-title `"` as part of any construct either — but curling it removes
// it from the byte stream entirely, so a *reparse* of the output finds
// the title's closing quote at the *next* `"` instead — the second
// link's own destination-closing quote, many bytes later — swallowing
// that second link's destination into a now-huge, bogus title and
// changing the render completely. Protecting broadly through the last
// same-line ")" — rather than trying to characterize exactly which
// quotes are "safe" to curl, a search at least as open-ended as
// segment.bracketedSpans's own destination/title grammar already proved
// to be (see its scanLinkDestTitle and naiveParenBalance) — closes this
// the same way htmlTagOpenerGuardSpans closes its tag analog: one
// generously wide answer instead of chasing individual shapes.
func linkParenOpenerGuardSpans(text string) []segment.Span {
	var out []segment.Span
	lineStart := 0
	for {
		lineEnd := len(text)
		if nl := strings.IndexByte(text[lineStart:], '\n'); nl >= 0 {
			lineEnd = lineStart + nl
		}
		line := text[lineStart:lineEnd]
		for _, loc := range linkParenOpenerRE.FindAllStringIndex(line, -1) {
			open := lineStart + loc[1] - 1 // the "(" byte itself
			end := lineEnd
			if gt := strings.LastIndexByte(text[open:lineEnd], ')'); gt >= 0 {
				end = open + gt + 1
			}
			out = append(out, segment.Span{Start: open, End: end})
		}
		if lineEnd == len(text) {
			break
		}
		lineStart = lineEnd + 1
	}
	return out
}

// precededByOddBackslashes reports whether text[pos] is immediately
// preceded by an odd number of backslashes — i.e. whether it is itself
// backslash-escaped. (segment.precededByOddBackslashes is the same
// predicate, unexported there; duplicating four lines is cheaper than
// widening that package's API for one caller.)
func precededByOddBackslashes(text string, pos int) bool {
	n := 0
	for n < pos && text[pos-1-n] == '\\' {
		n++
	}
	return n%2 == 1
}

// inProtected reports whether byte position pos falls inside any
// protected span.
func inProtected(spans []segment.Span, pos int) bool {
	for _, s := range spans {
		if pos >= s.Start && pos < s.End {
			return true
		}
	}
	return false
}

// anyProtected reports whether any byte position in [start,end) falls
// inside a protected span. Used for the multi-byte ellipsis run, so a
// protected range starting partway through the run still blocks it.
func anyProtected(spans []segment.Span, start, end int) bool {
	for _, s := range spans {
		if start < s.End && end > s.Start {
			return true
		}
	}
	return false
}
