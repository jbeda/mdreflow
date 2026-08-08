package typography

import (
	"testing"

	"github.com/jbeda/mdreflow/internal/segment"
)

// TestApply is a standalone table for the substitution heuristics,
// exercising Apply directly (no parser, no reflow pipeline) the way
// segment_test.go exercises the segmenter — so a heuristic regression
// shows up here, at the level it lives at, rather than as a golden-file
// diff several layers up.
func TestApply(t *testing.T) {
	both := SmartQuotes | Ellipses

	cases := []struct {
		name string
		opts Typography
		in   string
		want string
	}{
		// --- off ---
		{"no flags is a no-op", 0, `He said "hi"... it's fine.`, `He said "hi"... it's fine.`},
		{"ellipses only leaves quotes alone", Ellipses, `"a"... 'b'`, `"a"… 'b'`},
		{"smart quotes only leaves dots alone", SmartQuotes, `"a"... 'b'`, `“a”... ‘b’`},

		// --- double quotes ---
		{"opening at start of text", SmartQuotes, `"hello" there`, `“hello” there`},
		{"opening after whitespace", SmartQuotes, `he said "hello"`, `he said “hello”`},
		{"opening after open paren", SmartQuotes, `(a "quoted" word)`, `(a “quoted” word)`},
		{"closing after punctuation", SmartQuotes, `he said "hello." Then`, `he said “hello.” Then`},

		// --- single quotes and apostrophes ---
		{"contraction", SmartQuotes, `don't stop`, `don’t stop`},
		{"possessive", SmartQuotes, `the dog's bone`, `the dog’s bone`},
		{"plural possessive", SmartQuotes, `the dogs' bones`, `the dogs’ bones`},
		{"single quoted phrase", SmartQuotes, `she said 'wait' loudly`, `she said ‘wait’ loudly`},

		// --- decade abbreviations ---
		{"decade two digits", SmartQuotes, `back in the '90s`, `back in the ’90s`},
		{"decade at start of text", SmartQuotes, `'60s music`, `’60s music`},
		{"decade without trailing s", SmartQuotes, `the '90 model`, `the ’90 model`},
		{"decade followed by punctuation", SmartQuotes, `the '80s, mostly`, `the ’80s, mostly`},
		{"five digits is not a decade", SmartQuotes, `'12345 units`, `‘12345 units`},
		{"digits then letters is not a decade", SmartQuotes, `'90x thing`, `‘90x thing`},

		// --- nesting and markup boundaries ---
		{"nested single inside double", SmartQuotes, `"she said 'hi' to me"`, `“she said ‘hi’ to me”`},
		{"double inside bold delimiters", SmartQuotes, `**"quoted"**`, `**“quoted”**`},
		{"single inside emphasis delimiters", SmartQuotes, `*'quoted'*`, `*‘quoted’*`},
		{"double inside underscore emphasis", SmartQuotes, `_"quoted"_`, `_“quoted”_`},

		// --- documented naive cases ---
		{"prime marks are curled (naive, documented)", SmartQuotes, `6'2" tall`, `6’2” tall`},
		{"leading elision is opened (naive, documented)", SmartQuotes, `rock 'n' roll`, `rock ‘n’ roll`},

		// --- ellipsis ---
		{"exactly three dots", Ellipses, `wait... what`, `wait… what`},
		{"three dots at end of text", Ellipses, `trailing off...`, `trailing off…`},
		{"two dots untouched", Ellipses, `a .. b`, `a .. b`},
		{"four dots untouched", Ellipses, `a.... b`, `a.... b`},
		{"five dots untouched", Ellipses, `a..... b`, `a..... b`},
		{"two separate runs of three", Ellipses, `a... b... c`, `a… b… c`},
		{"already substituted is untouched", Ellipses, `a… b`, `a… b`},

		// --- combined ---
		{"both flags together", both, `He said "wait..." and she didn't.`, `He said “wait…” and she didn’t.`},

		// --- backslash escapes ---
		{"escaped double quote untouched", both, `a \" b`, `a \" b`},
		{"escaped single quote untouched", both, `a \' b`, `a \' b`},
		{"escaped first dot blocks the ellipsis", both, `a \... b`, `a \... b`},
		// An even run of backslashes escapes only itself, so the quote
		// after it is a real quote and is substituted. Its direction is
		// decided by the literal backslash in front of it, which is not
		// opening punctuation — so it closes. Defensible either way and
		// vanishingly rare; recorded here rather than special-cased.
		{"escaped backslash before a quote still substitutes", both, `a \\"b"`, `a \\”b”`},

		// --- idempotency at this level ---
		{"curly input is untouched", both, `“a” ‘b’ c… d`, `“a” ‘b’ c… d`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Apply(tc.in, nil, tc.opts)
			if got != tc.want {
				t.Errorf("Apply(%q, nil, %d) = %q, want %q", tc.in, tc.opts, got, tc.want)
			}
			// Every case must also be a fixed point of a second pass:
			// Apply's output is what a later Format pass reads back as
			// input, so a non-idempotent rule here is an idempotency
			// break for the whole tool.
			if again := Apply(got, nil, tc.opts); again != got {
				t.Errorf("Apply is not idempotent: Apply(%q) = %q", got, again)
			}
		})
	}
}

// TestApplyRespectsProtectedSpans checks the protection contract against
// real segment.NoBreakSpans output: the substitutions must never reach
// inside inline code, a link (text or destination), an autolink, inline
// math, a shortcode, an {expr} span, or an inline HTML tag.
func TestApplyRespectsProtectedSpans(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "inline code span",
			in:   "prose \"a\" and `code \"b\"... here` and \"c\"...",
			want: "prose “a” and `code \"b\"... here` and “c”…",
		},
		{
			name: "link text and destination",
			in:   `see [the "docs"](https://e.com/a...b) and "this"...`,
			want: `see [the "docs"](https://e.com/a...b) and “this”…`,
		},
		{
			name: "autolink",
			in:   `at <https://e.com/?q="x"...> and "y"`,
			want: `at <https://e.com/?q="x"...> and “y”`,
		},
		{
			name: "inline math",
			in:   `math $a'b...c$ but "prose"`,
			want: `math $a'b...c$ but “prose”`,
		},
		{
			name: "hugo shortcode",
			in:   `code {{< ref "p...md" >}} but "prose"`,
			want: `code {{< ref "p...md" >}} but “prose”`,
		},
		{
			name: "mdx expr span",
			in:   `expr {a "b"...} but "prose"`,
			want: `expr {a "b"...} but “prose”`,
		},
		{
			name: "inline html tag attributes",
			in:   `<span class="note">but "this" is prose</span>`,
			want: `<span class="note">but “this” is prose</span>`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Apply(tc.in, segment.NoBreakSpans(tc.in), SmartQuotes|Ellipses)
			if got != tc.want {
				t.Errorf("Apply(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestApplyProtectsBareLinks covers the protection Apply supplies itself
// rather than taking from the caller: GFM linkify's undelimited bare URLs
// and email addresses, where a substituted byte silently changes the
// link's destination (see bareLinkSpans).
func TestApplyProtectsBareLinks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "quote inside a bare URL",
			in:   `http://0.a#020"0`,
			want: `http://0.a#020"0`,
		},
		{
			name: "dots inside a bare URL",
			in:   `see http://a.com/x...y here`,
			want: `see http://a.com/x...y here`,
		},
		{
			name: "apostrophe inside a bare URL path",
			in:   `see http://a.com/it's/path here`,
			want: `see http://a.com/it's/path here`,
		},
		{
			// No path component, so goldmark's domain scan stops at the
			// quote and it is real prose: trimmed here and curled.
			name: "trailing quote after a bare domain still curls",
			in:   `He said "see http://a.com" now`,
			want: `He said “see http://a.com” now`,
		},
		{
			// A fragment starts a path component, so goldmark swallows
			// the quote into the destination: it must stay straight.
			name: "trailing quote after a fragment is part of the link",
			in:   `http://0.a#"`,
			want: `http://0.a#"`,
		},
		{
			name: "trailing quote after a path is part of the link",
			in:   `http://a.com/x"`,
			want: `http://a.com/x"`,
		},
		{
			name: "www prefixed",
			in:   `at www.a.com/x"y and "prose"`,
			want: `at www.a.com/x"y and “prose”`,
		},
		{
			name: "bare email address",
			in:   `mail a'b@example.com about "it"`,
			want: `mail a'b@example.com about “it”`,
		},
		{
			name: "prose around a URL is still substituted",
			in:   `"See http://a.com for more"...`,
			want: `“See http://a.com for more”…`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Apply(tc.in, segment.NoBreakSpans(tc.in), SmartQuotes|Ellipses)
			if got != tc.want {
				t.Errorf("Apply(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestApplyProtectedEllipsisPartialOverlap checks that a three-dot run
// only partly covered by a protected span is left entirely alone, rather
// than being half-rewritten (anyProtected, not inProtected, gates the
// ellipsis rule for exactly this reason).
func TestApplyProtectedEllipsisPartialOverlap(t *testing.T) {
	// The code span starts one byte into the three-dot run.
	in := ".`..x`"
	if got := Apply(in, segment.NoBreakSpans(in), Ellipses); got != in {
		t.Errorf("Apply(%q) = %q, want it unchanged", in, got)
	}
}
