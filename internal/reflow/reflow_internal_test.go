package reflow

import "testing"

// TestIsThematicBreak tables the CommonMark thematic-break rule documented
// on isThematicBreak's own doc comment: up to 3 leading spaces, then 3+ of
// the same '-'/'*'/'_' character, optionally interspersed with
// spaces/tabs.
func TestIsThematicBreak(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"three dashes", "---", true},
		{"three asterisks", "***", true},
		{"three underscores", "___", true},
		{"two dashes not enough", "--", false},
		{"spaced dashes", "- - -", true},
		{"leading 3 spaces still counts", "   ---", true},
		{"leading 4 spaces disqualifies", "    ---", false},
		{"tabs between markers allowed", "-\t-\t-", true},
		{"mixed characters disqualify", "-*-", false},
		{"trailing non-marker text disqualifies", "--- foo", false},
		{"empty string", "", false},
		{"only spaces", "   ", false},
		{"single dash", "-", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isThematicBreak(tc.line); got != tc.want {
				t.Errorf("isThematicBreak(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// TestIsSetextUnderline tables isSetextUnderline's doc comment: up to 3
// leading spaces, then 1+ '=' characters, optionally with trailing
// spaces/tabs — no minimum-repeat requirement, unlike a thematic break.
func TestIsSetextUnderline(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"single equals", "=", true},
		{"multiple equals", "====", true},
		{"leading 3 spaces still counts", "   =", true},
		// NOTE: doc comment says only up to 3 *leading* spaces are ignored,
		// but the implementation's scan loop tolerates ' '/'\t' bytes
		// anywhere in the string (not just leading/trailing) without
		// disqualifying the match — so a 4th leading space is simply
		// treated as this loop's own tolerated interior whitespace, not a
		// disqualifying byte. Pinning actual behavior here; see this
		// package's test-writing report for the doc/implementation gap
		// (suspected bug, not fixed per review ground rules).
		{"leading 4 spaces: doc says disqualifies, but loop tolerates any interior space/tab so this is still true", "    =", true},
		{"space between equals also tolerated (same doc/impl gap)", "= =", true},
		{"trailing spaces allowed", "=== ", true},
		{"trailing tab allowed", "=\t", true},
		{"trailing CR allowed (goldmark treats it as trailing whitespace)", "=\r", true},
		{"non-equals character disqualifies", "=a", false},
		{"dashes are not setext level-1", "---", false},
		{"empty string", "", false},
		{"only spaces", "   ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSetextUnderline(tc.line); got != tc.want {
				t.Errorf("isSetextUnderline(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// TestIsFenceOpener tables isFenceOpener's doc comment: a 3+ backtick run
// disqualifies if its info string contains a backtick; a 3+ tilde run's
// info string is unrestricted.
func TestIsFenceOpener(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"three backticks bare", "```", true},
		{"backtick fence with info string", "```go", true},
		{"backtick fence info string contains backtick disqualifies", "```go`", false},
		{"two backticks not enough", "``", false},
		{"three tildes bare", "~~~", true},
		{"tilde fence info string may contain backtick", "~~~go`", true},
		{"tilde fence with backtick info", "~~~`go`", true},
		{"two tildes not enough", "~~", false},
		{"neither backtick nor tilde", "abc", false},
		{"empty string", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isFenceOpener(tc.line); got != tc.want {
				t.Errorf("isFenceOpener(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// TestFenceOpenerRunLen checks the leading backtick/tilde run length
// isFenceOpener and escapeBlockInterrupt's fence branch both rely on.
func TestFenceOpenerRunLen(t *testing.T) {
	cases := []struct {
		name string
		line string
		want int
	}{
		{"three backticks", "```", 3},
		{"three backticks plus info", "```go", 3},
		{"five tildes", "~~~~~go", 5},
		{"mixed run counts all", "``~~go", 4},
		{"no run", "go```", 0},
		{"empty", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fenceOpenerRunLen(tc.line); got != tc.want {
				t.Errorf("fenceOpenerRunLen(%q) = %d, want %d", tc.line, got, tc.want)
			}
		})
	}
}

// TestIsTableDelimiterRowShaped tables isTableDelimiterRowShaped's doc
// comment: once trimmed, the line must contain only "-", ":", "|",
// spaces, and tabs, with at least one "-".
func TestIsTableDelimiterRowShaped(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"bare dash pipe", "-|", true},
		{"colon dash", ":-", true},
		{"full row", "-|-|-|-|-|-|-", true},
		{"issue 13 shape", "-:", true},
		{"spaces within shape allowed", "-- |", true},
		{"leading trailing whitespace trimmed", "  -|  ", true},
		{"CR within shape allowed (goldmark cell-padding whitespace, seed d994c9196409b0fd)", "|-\r|", true},
		{"only colons and pipes no dash", ":|:", false},
		{"only whitespace", "   ", false},
		{"empty string", "", false},
		{"contains other punctuation", "-!", false},
		{"contains letter", "-a", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTableDelimiterRowShaped(tc.line); got != tc.want {
				t.Errorf("isTableDelimiterRowShaped(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

// TestTrailingBackslashCount tables trailingBackslashCount's doc comment:
// count of consecutive trailing backslashes.
func TestTrailingBackslashCount(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want int
	}{
		{"none", "abc", 0},
		{"one", `abc\`, 1},
		{"two", `abc\\`, 2},
		{"three", `abc\\\`, 3},
		{"empty string", "", 0},
		{"all backslashes", `\\\`, 3},
		{"backslash mid string not counted", `a\bc`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trailingBackslashCount(tc.s); got != tc.want {
				t.Errorf("trailingBackslashCount(%q) = %d, want %d", tc.s, got, tc.want)
			}
		})
	}
}

// TestCanonicalizeForWidth tables canonicalizeForWidth's doc comment: a
// space/tab/CR run containing a real space or tab collapses to one space
// when it is 2+ bytes wide and not inside a no-break span; a pure-CR run,
// or a single-byte run, is left untouched.
func TestCanonicalizeForWidth(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"single space unchanged", "a b", "a b"},
		{"double space collapses", "a  b", "a b"},
		{"tab run collapses", "a\t\tb", "a b"},
		{"mixed space tab cr collapses", "a \t\r b", "a b"},
		{"pure cr run not collapsed (no real space/tab core)", "a\r\rb", "a\r\rb"},
		{"single cr not collapsed", "a\rb", "a\rb"},
		{"run inside inline code not collapsed", "`a  b`", "`a  b`"},
		{"no whitespace unchanged", "abc", "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := canonicalizeForWidth(tc.text); got != tc.want {
				t.Errorf("canonicalizeForWidth(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

// TestDetectHardBreak tables detectHardBreak's doc comment: the three
// hard-break syntaxes (trailing backslash, trailing double-space,
// trailing <br>), each gated by isLastLine and insideSpan, the astHard
// AST-confirmation gate on the backslash and double-space spellings, plus
// the backslash-escape awareness for <br>.
func TestDetectHardBreak(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		isLastLine bool
		insideSpan bool
		astHard    bool
		wantMarker string
		wantRest   string
	}{
		{"inside code span never a break", "foo\\", false, true, true, "", "foo\\"},
		{"single trailing backslash not last line", "foo\\", false, false, true, "\\", "foo"},
		{"single trailing backslash on last line not a break", "foo\\", true, false, false, "", "foo\\"},
		{"three trailing backslashes not a break (M1 rule reversed)", `foo\\\`, false, false, false, "", `foo\\\`},
		// Whitespace before the backslash goes with the marker, matching
		// the other two syntaxes — otherwise pass 2, which sees the
		// preserved backslash spelling, trims it and disagrees with pass 1
		// (seed d4274cf2d1364325).
		{"whitespace before the backslash consumed with the marker", "foo \\", false, false, true, "\\", "foo"},
		{"tab before the backslash likewise", "foo\t\\", false, false, true, "\\", "foo"},
		{"two trailing spaces not last line, promoted to backslash", "foo  ", false, false, true, "\\", "foo"},
		{"two trailing spaces on last line insignificant", "foo  ", true, false, false, "", "foo  "},
		{"three trailing spaces still a break, promoted to backslash", "foo   ", false, false, true, "\\", "foo"},
		{"one trailing space not enough", "foo ", false, false, false, "", "foo "},
		// The AST veto (#39): raw trailing bytes spell a break, but
		// goldmark judged the line ending soft — an inline extension
		// consumed the line's prose and left no carrier for the flag
		// ("* [X]  \n0"). The raw spelling must not be trusted.
		{"double space vetoed when AST says soft break", "[X]  ", false, false, false, "", "[X]  "},
		{"trailing backslash vetoed when AST says soft break", "foo\\", false, false, false, "", "foo\\"},
		{"br tag always recognized even on last line", "foo<br>", true, false, false, "<br>", "foo"},
		{"br tag recognized without AST confirmation (raw HTML carries no flag)", "foo<br>", false, false, false, "<br>", "foo"},
		{"br tag with whitespace before it consumed", "foo <br>", false, false, false, "<br>", "foo"},
		{"br tag case insensitive and self closing, respelled canonically", "foo<BR/>", false, false, false, "<br>", "foo"},
		{"escaped br tag not a break", `foo\<br>`, false, false, false, "", `foo\<br>`},
		{"br tag with separating space before backslash still a break, space consumed with the marker", `foo\ <br>`, false, false, false, "<br>", `foo\`},
		{"no hard break syntax at all", "plain text", false, false, false, "", "plain text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			marker, rest := detectHardBreak(tc.content, tc.isLastLine, tc.insideSpan, tc.astHard)
			if marker != tc.wantMarker || rest != tc.wantRest {
				t.Errorf("detectHardBreak(%q, isLastLine=%v, insideSpan=%v, astHard=%v) = (%q, %q), want (%q, %q)",
					tc.content, tc.isLastLine, tc.insideSpan, tc.astHard, marker, rest, tc.wantMarker, tc.wantRest)
			}
		})
	}
}

// TestEscapeBlockInterrupt tables escapeBlockInterrupt's doc comment: the
// backslash/entity escape for each trigger class, gated correctly by
// isFirstLine, firstLinePrefix (the joint thematic-break case), and
// prevLineNonBlank (the joint table-delimiter-row case).
func TestEscapeBlockInterrupt(t *testing.T) {
	cases := []struct {
		name             string
		line             string
		isFirstLine      bool
		firstLinePrefix  string
		prevLineNonBlank bool
		want             string
	}{
		{"empty line unchanged", "", false, "", true, ""},
		{"plain prose unchanged", "hello world", false, "", true, "hello world"},

		{"atx heading escaped", "# heading", false, "", true, "\\# heading"},
		{"atx heading bare hash escaped", "#", false, "", true, "\\#"},
		{"non-heading hash run not escaped", "#nospace", false, "", true, "#nospace"},

		{"blockquote marker escaped", "> quote", false, "", true, "\\> quote"},

		{"bullet marker escaped", "- item", false, "", true, "\\- item"},
		{"plus bullet marker escaped", "+ item", false, "", true, "\\+ item"},
		{"star bullet marker escaped", "* item", false, "", true, "\\* item"},
		{"dash not followed by space not a bullet trigger, escaped via thematic/other rule only if applicable", "-word", false, "", true, "-word"},

		{"ordered list marker escapes the delimiter not the digit", "1. item", false, "", true, "1\\. item"},
		{"ordered list paren delimiter escaped", "1) item", false, "", true, "1\\) item"},
		{"ordered list bare marker escaped", "1.", false, "", true, "1\\."},
		{"non-marker leading digit run not escaped", "123abc", false, "", true, "123abc"},

		{"html comment opener escaped", "<!-- x -->", false, "", true, "\\<!-- x -->"},
		{"processing instruction escaped", "<?php ?>", false, "", true, "\\<?php ?>"},
		{"known html block tag escaped", "<div>", false, "", true, "\\<div>"},
		{"unknown tag not escaped mid paragraph (type 7, not first line)", "<xyz>", false, "", true, "<xyz>"},
		{"unknown tag escaped on first line only (type 7)", "<xyz>", true, "", true, "\\<xyz>"},
		{"literal inline br not escaped mid paragraph (type 7 only checked on first line)", "<br>", false, "", true, "<br>"},
		{"bare br tag alone on first line IS escaped (type 7 applies to any tag name, recognized or not)", "<br>", true, "", true, "\\<br>"},

		{"thematic break escaped", "---", false, "", true, "\\---"},
		{"thematic break joint with container prefix escaped", "--", false, "-", true, "\\--"},
		{"two-char run alone is not thematic break, bullet, or table-shaped", "**", false, "", true, "**"},

		{"setext underline escaped", "===", false, "", true, "\\==="},

		{"fence opener backtick escaped as entity", "```go", false, "", true, "&#96;&#96;&#96;go"},
		{"fence opener tilde escaped with backslash", "~~~go", false, "", true, "\\~\\~\\~go"},

		{"link ref def opener escaped", "[foo]: /url", false, "", true, "\\[foo]: /url"},

		{"table delimiter row escaped when prev line non-blank", "-|-", false, "", true, "\\-|-"},
		{"table delimiter row not escaped when prev line blank", "-|-", false, "", false, "-|-"},

		{"no trigger matches text left alone", "just some text.", false, "", true, "just some text."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := escapeBlockInterrupt(tc.line, tc.isFirstLine, tc.firstLinePrefix, tc.prevLineNonBlank)
			if got != tc.want {
				t.Errorf("escapeBlockInterrupt(%q, isFirstLine=%v, firstLinePrefix=%q, prevLineNonBlank=%v) = %q, want %q",
					tc.line, tc.isFirstLine, tc.firstLinePrefix, tc.prevLineNonBlank, got, tc.want)
			}
		})
	}
}
