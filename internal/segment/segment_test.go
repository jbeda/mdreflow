package segment

import (
	"reflect"
	"testing"
)

// TestBreaks is a Golden-Rules-style table of standalone Breaks cases (in
// the spirit of pragmatic_segmenter's golden rules, but our own), covering
// the exceptions and guards documented on Segmenter.Breaks.
func TestBreaks(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []Span
	}{
		{
			name: "simple two sentences",
			text: "This is one. This is two.",
			want: []Span{{Start: 12, End: 13}},
		},
		{
			name: "three sentences",
			text: "One. Two. Three.",
			want: []Span{{Start: 4, End: 5}, {Start: 9, End: 10}},
		},
		{
			name: "no terminal punctuation",
			text: "Just one sentence with no end",
			want: nil,
		},
		{
			name: "exclamation and question marks",
			text: "Wait! Really? Yes.",
			want: []Span{{Start: 5, End: 6}, {Start: 13, End: 14}},
		},
		{
			name: "abbreviation Mr not a break",
			text: "Mr. Smith arrived.",
			want: nil,
		},
		{
			name: "abbreviation followed by more text",
			text: "Dr. Jones and Mrs. Jones came. Then they left.",
			want: []Span{{Start: 30, End: 31}},
		},
		{
			name: "e.g. mid sentence",
			text: "Bring supplies, e.g. rope and water, before noon.",
			want: nil,
		},
		{
			name: "etc. followed by capitalized next sentence still suppressed",
			text: "Pack food, water, etc. We leave soon.",
			want: nil,
		},
		{
			name: "decimal point never a candidate",
			text: "The value is 3.14 exactly.",
			want: nil,
		},
		{
			name: "ellipsis followed by lowercase does not split",
			text: "Wait... what happens next.",
			want: nil,
		},
		{
			name: "ellipsis followed by uppercase does split",
			text: "Hold on... Something changed.",
			want: []Span{{Start: 10, End: 11}},
		},
		{
			name: "closing double quote after period",
			text: `She said "I am done." Then she left.`,
			want: []Span{{Start: 21, End: 22}},
		},
		{
			name: "closing paren after period",
			text: "This is true (I checked it.) Moving on.",
			want: []Span{{Start: 28, End: 29}},
		},
		{
			name: "trailing punctuation with no following text",
			text: "This is the only sentence.",
			want: nil,
		},
		{
			name: "next char lowercase suppresses break",
			text: "This looks like a break. but is not capitalized.",
			want: nil,
		},
		{
			name: "next char digit starts a sentence",
			text: "See section one. 2 is the next section.",
			want: []Span{{Start: 16, End: 17}},
		},
		{
			name: "next char opening quote starts a sentence",
			text: `He finished. "Now what?" She asked.`,
			want: []Span{{Start: 12, End: 13}, {Start: 24, End: 25}},
		},
		{
			name: "U.S. abbreviation with internal periods",
			text: "She works for the U.S. government now.",
			want: nil,
		},
		{
			name: "multiple spaces after period collapse into one gap",
			text: "First sentence.   Second sentence.",
			want: []Span{{Start: 15, End: 18}},
		},
		{
			name: "abbreviation at very end of text",
			text: "They packed supplies, etc.",
			want: nil,
		},
		{
			name: "period directly followed by lowercase word no space",
			text: "Visit example.com for more info.",
			want: nil,
		},
		{
			// Mined regression class: sentence-ending punctuation inside a
			// parenthetical aside, itself followed by a genuine sentence
			// end — pragmatic_segmenter's "parenthesis" golden rule.
			name: "parenthetical aside with its own terminal punctuation",
			text: "He said it plainly (there was no doubt about it). Nobody argued.",
			want: []Span{{Start: 49, End: 50}},
		},
		{
			// Mined regression class: a quotation containing terminal
			// punctuation, followed by a genuine sentence end outside the
			// quote — pragmatic_segmenter's "quotation" golden rule.
			name: "quotation containing its own period",
			text: `She asked, "Is this really it?" He nodded and left.`,
			want: []Span{{Start: 31, End: 32}},
		},
		{
			// Mined regression class: multi-part initials (a name written
			// as consecutive single-letter abbreviations) must not each be
			// treated as sentence ends.
			name: "multi-part initials in a name",
			text: "J. R. R. Tolkien wrote it. Everyone agreed.",
			want: []Span{{Start: 26, End: 27}},
		},
		{
			// Mined regression class: an ordinal/decimal-looking number
			// mid-sentence must not be confused with a list marker or a
			// sentence end.
			name: "decimal number mid sentence not a list marker",
			text: "The part costs 3.5 dollars. That is the final price.",
			want: []Span{{Start: 27, End: 28}},
		},
	}

	seg := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := seg.Breaks(tc.text)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Breaks(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestBreaksSingleInitial isolates the "J. Beda" case (a malformed
// duplicate entry above was left out of the table intentionally): the
// initial itself must not produce a break, but a genuine sentence end
// later in the text still does.
func TestBreaksSingleInitial(t *testing.T) {
	seg := New(nil)
	text := "J. Beda wrote this. It is a test."
	got := seg.Breaks(text)
	want := []Span{{Start: 19, End: 20}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Breaks(%q) = %v, want %v", text, got, want)
	}
}

// TestBreaksCustomAbbreviations checks that Options.Abbreviations-style
// additions (via New's extra parameter) suppress a break that the default
// list alone would not.
func TestBreaksCustomAbbreviations(t *testing.T) {
	seg := New([]string{"cont."})
	text := "See page 12 cont. For details, read the appendix."
	if got := seg.Breaks(text); got != nil {
		t.Errorf("Breaks(%q) = %v, want nil (custom abbreviation should suppress the break)", text, got)
	}

	// Without the custom addition, the same text does break.
	def := New(nil)
	if got := def.Breaks(text); len(got) != 1 {
		t.Errorf("Breaks(%q) with default abbreviations = %v, want exactly one break", text, got)
	}
}

// TestIsBoundaryWhitespaceByte tables isBoundaryWhitespaceByte's doc
// comment: space, tab, and bare '\r' are boundary whitespace; nothing
// else is, including '\n' (Breaks is only ever called on already-joined,
// newline-free cluster text).
func TestIsBoundaryWhitespaceByte(t *testing.T) {
	cases := []struct {
		name string
		b    byte
		want bool
	}{
		{"space", ' ', true},
		{"tab", '\t', true},
		{"bare carriage return", '\r', true},
		{"newline is not boundary whitespace", '\n', false},
		{"ordinary letter", 'A', false},
		{"digit", '0', false},
		{"punctuation", '.', false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBoundaryWhitespaceByte(tc.b); got != tc.want {
				t.Errorf("isBoundaryWhitespaceByte(%q) = %v, want %v", tc.b, got, tc.want)
			}
		})
	}
}

// TestBreaksBareCarriageReturn covers issue #10's fix: a bare '\r'
// (unpaired with '\n', so it survives as literal content mid-cluster)
// sitting directly against a token must be treated as boundary whitespace
// on both the backward token-boundary scan (so a single-initial like "A."
// is still recognized as one, not swept into "\rA.") and the forward
// post-punctuation whitespace-run scan (so the '\r' is consumed as part
// of the break, not left dangling for the sentence-start guard to choke
// on).
func TestBreaksBareCarriageReturn(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []Span
	}{
		{
			// Backward scan: '\r' must stop the token scan the same way
			// ' ' does, so "A." reads as a single-initial (no break), not
			// "\rA." (which would not match isSingleInitial and so would
			// wrongly produce a break).
			name: "bare CR before a single initial suppresses the break",
			text: "pad \rA. BC",
			want: nil,
		},
		{
			// Forward scan: '\r' in the post-punctuation whitespace run is
			// consumed as part of the break, not left as a leading byte
			// the sentence-start guard has to see.
			name: "bare CR in the post-punctuation whitespace run is consumed",
			text: "One.\rTwo.",
			want: []Span{{Start: 4, End: 5}},
		},
	}
	seg := New(nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := seg.Breaks(tc.text)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Breaks(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestDefaultAbbreviationsIsACopy(t *testing.T) {
	a := DefaultAbbreviations()
	a[0] = "mutated"
	b := DefaultAbbreviations()
	if b[0] == "mutated" {
		t.Fatal("DefaultAbbreviations must return a fresh copy each call")
	}
}
