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
