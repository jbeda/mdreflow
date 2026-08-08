package mdreflow_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/yuin/goldmark/text"

	"github.com/jbeda/mdreflow/internal/gm"

	"github.com/jbeda/mdreflow"
)

// fixtures returns the base name (without extension) of every golden pair
// in testdata/: <name>.md plus <name>.golden.md.
func fixtures(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("testdata/*.golden.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no golden fixtures found under testdata/")
	}
	names := make([]string, len(matches))
	for i, m := range matches {
		base := filepath.Base(m)
		names[i] = base[:len(base)-len(".golden.md")]
	}
	return names
}

// TestGoldenFixtures runs every testdata/*.md fixture through Format and
// diffs it against the matching *.golden.md, then checks the two
// guarantees that hold automatically for every fixture: idempotency and
// render preservation.
func TestGoldenFixtures(t *testing.T) {
	for _, name := range fixtures(t) {
		t.Run(name, func(t *testing.T) {
			runGoldenCase(t, filepath.Join("testdata", name+".md"), filepath.Join("testdata", name+".golden.md"), mdreflow.Options{})
		})
	}
}

// runGoldenCase formats srcPath under opts, diffs the result against
// goldenPath, and checks the two guarantees that hold automatically for
// every fixture regardless of mode: idempotency and render preservation.
func runGoldenCase(t *testing.T, srcPath, goldenPath string, opts mdreflow.Options) {
	t.Helper()
	src, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}

	got, err := mdreflow.Format(src, opts)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if !bytes.Equal(got, golden) {
		t.Errorf("Format(%s) does not match golden.\n--- got ---\n%s\n--- want ---\n%s", srcPath, got, golden)
	}

	t.Run("idempotent", func(t *testing.T) {
		twice, err := mdreflow.Format(got, opts)
		if err != nil {
			t.Fatalf("Format(Format(x)): %v", err)
		}
		if !bytes.Equal(twice, got) {
			t.Errorf("Format is not idempotent.\n--- Format(x) ---\n%s\n--- Format(Format(x)) ---\n%s", got, twice)
		}
	})

	t.Run("render-preserving", func(t *testing.T) {
		before := normalizeForRender(renderHTML(t, src), opts)
		after := normalizeForRender(renderHTML(t, got), opts)
		if before != after {
			t.Errorf("rendered HTML changed.\n--- before ---\n%s\n--- after ---\n%s", before, after)
		}
	})
}

// typographyNormalizer maps every character the typography substitutions
// can *produce* back to the ASCII it was produced from. Both curly
// spellings of a quote collapse onto the one straight character, and
// "…" back onto three periods; "&quot;" and "&#39;" are folded in too,
// since goldmark's HTML renderer escapes a straight quote in text
// content but leaves the curly ones alone, so the two sides of a
// comparison would otherwise differ purely in escaping.
var typographyNormalizer = strings.NewReplacer(
	"&quot;", `"`,
	"&#39;", "'",
	"“", `"`,
	"”", `"`,
	"‘", "'",
	"’", "'",
	"…", "...",
)

// normalizeForRender applies normalizeWhitespace and, when opts enables
// typography, also folds the typography substitutions back out of the
// rendered HTML before comparison.
//
// docs/design.md names typography as *the* documented exception to
// render preservation, and it is a different kind of exception from the
// narrow byte-pattern escape hatches in fuzz_test.go: it is not a rare
// shape that happens to break, it is the entire point of the flag, and
// every document containing a quote or three periods renders differently
// with it on. Skipping the render check outright for typography-enabled
// runs would therefore throw away the check on precisely the runs that
// change the most content. Normalizing instead keeps it: the mapping is
// many-to-one and applied identically to both sides, so it can mask
// exactly the substitutions typography is licensed to make and nothing
// else — a quote that reflow *lost*, moved, or duplicated still fails
// the comparison, as does any other content change.
//
// The alternative considered was to assert only against the golden
// files. That is weaker, not stronger: goldens pin the bytes for the six
// typography fixtures, whereas this keeps the corpus-wide property
// (TestTypographyOverCorpus) meaningful across every fixture in the
// repository.
func normalizeForRender(html string, opts mdreflow.Options) string {
	s := normalizeWhitespace(html)
	if opts.Typography != 0 {
		s = typographyNormalizer.Replace(s)
	}
	return s
}

// modeWidthRE extracts a fixture's encoded MaxWidth from its base name:
// wrap and sentence-maxwidth fixtures are named "w<N>-..." /
// "mw<N>-...", the number their family's shared generator (and this test)
// both read as Options.MaxWidth. Fixtures whose behavior doesn't depend on
// a specific width (e.g. the para family) need no such prefix.
var modeWidthRE = regexp.MustCompile(`^m?w(\d+)-`)

// TestGoldenFixturesModes runs the mode-specific fixture families under
// testdata/modes/<family>/ through the same golden/idempotency/render-
// preservation checks as TestGoldenFixtures, each under the Options that
// family exercises: para mode (no width), wrap mode (width encoded in the
// filename as "w<N>-..."), and sentence mode with MaxWidth (width encoded
// as "mw<N>-...").
func TestGoldenFixturesModes(t *testing.T) {
	families := []struct {
		dir     string
		optsFor func(t *testing.T, base string) mdreflow.Options
	}{
		{"para", func(t *testing.T, base string) mdreflow.Options {
			return mdreflow.Options{Mode: mdreflow.ModePara}
		}},
		{"wrap", func(t *testing.T, base string) mdreflow.Options {
			return mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: mustModeWidth(t, base)}
		}},
		{"sentence-maxwidth", func(t *testing.T, base string) mdreflow.Options {
			return mdreflow.Options{MaxWidth: mustModeWidth(t, base)}
		}},
	}

	for _, fam := range families {
		t.Run(fam.dir, func(t *testing.T) {
			dir := filepath.Join("testdata", "modes", fam.dir)
			matches, err := filepath.Glob(filepath.Join(dir, "*.golden.md"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) == 0 {
				t.Fatalf("no golden fixtures found under %s", dir)
			}
			for _, goldenPath := range matches {
				base := filepath.Base(goldenPath)
				name := base[:len(base)-len(".golden.md")]
				t.Run(name, func(t *testing.T) {
					srcPath := filepath.Join(dir, name+".md")
					opts := fam.optsFor(t, name)
					runGoldenCase(t, srcPath, goldenPath, opts)
				})
			}
		})
	}
}

// typographyPrefixes maps a testdata/typography/ fixture's filename
// prefix to the Typography bits that family exercises. The prefix
// encoding follows the same convention the mode families use for
// MaxWidth ("w<N>-"): the fixture name states the option it was
// generated under, so a fixture and its golden can never drift apart
// from the options the test runs them with.
var typographyPrefixes = []struct {
	prefix string
	bits   mdreflow.Typography
}{
	{"off-", 0},
	{"sq-", mdreflow.SmartQuotes},
	{"el-", mdreflow.Ellipses},
	{"both-", mdreflow.SmartQuotes | mdreflow.Ellipses},
}

// TestGoldenFixturesTypography runs testdata/typography/ through the same
// golden/idempotency/render checks as the other fixture families, under
// the typography flags each fixture's name prefix encodes. The "off-"
// family is the control: with no flags set, quotes and periods must pass
// through byte-for-byte.
func TestGoldenFixturesTypography(t *testing.T) {
	dir := filepath.Join("testdata", "typography")
	matches, err := filepath.Glob(filepath.Join(dir, "*.golden.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatalf("no golden fixtures found under %s", dir)
	}
	for _, goldenPath := range matches {
		base := filepath.Base(goldenPath)
		name := base[:len(base)-len(".golden.md")]
		t.Run(name, func(t *testing.T) {
			runGoldenCase(t, filepath.Join(dir, name+".md"), goldenPath, mdreflow.Options{
				Typography: mustTypographyBits(t, name),
			})
		})
	}
}

// mustTypographyBits resolves a typography fixture's name prefix to its
// Typography bits, failing the test if the fixture is misnamed (the same
// contract mustModeWidth enforces for the mode families).
func mustTypographyBits(t *testing.T, base string) mdreflow.Typography {
	t.Helper()
	for _, p := range typographyPrefixes {
		if strings.HasPrefix(base, p.prefix) {
			return p.bits
		}
	}
	t.Fatalf("fixture %q has no recognized typography name prefix (want one of off-, sq-, el-, both-)", base)
	return 0
}

// TestParagraphAdjacentToLinkRefDefPassesThrough pins design.md's blunt
// link-reference-definition zone rule (superseding the driver-review
// regression this test used to pin — an earlier version of blockmap's
// precededByLinkRefDef guard skipped reflowing *every* paragraph directly
// after *any* link reference definition, including the common
// self-complete case). design.md's replacement rule is even blunter: ANY
// paragraph sitting directly against (no blank line) a line opening with a
// "[label]:" shape passes through byte-for-byte, self-complete or not — no
// adjacency analysis to tell the two apart, see "The link-reference-
// definition zone: skip bluntly, by shape". testdata/link-ref-def-no-blank.md
// golden-pins the same two shapes.
func TestParagraphAdjacentToLinkRefDefPassesThrough(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{
			name: "bare destination",
			src:  "[foo]: /url\nThis is a long paragraph. It has two sentences that should reflow.",
		},
		{
			name: "destination with title",
			src:  "[foo]: /url \"Title\"\nThis is a long paragraph. It has two sentences that should reflow.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mdreflow.Format([]byte(tc.src), mdreflow.Options{})
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			if string(got) != tc.src {
				t.Errorf("Format(%q) = %q, want unchanged (zone pass-through)", tc.src, got)
			}
		})
	}
}

// corpusFixtures returns every fixture input in testdata/ — the top-level
// family, the mode families, and the typography family — as paths.
func corpusFixtures(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, pattern := range []string{
		filepath.Join("testdata", "*.md"),
		filepath.Join("testdata", "modes", "*", "*.md"),
		filepath.Join("testdata", "typography", "*.md"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, matches...)
	}
	if len(out) == 0 {
		t.Fatal("no fixtures found under testdata/")
	}
	return out
}

// TestTypographyOverCorpus is the fixtures-by-options property loop
// docs/design.md's Testing section item 2 calls for, extended to the
// typography flags: every fixture in the repository (goldens included,
// since a golden is itself a valid input) is formatted under every
// typography combination, in every mode, and must satisfy the two
// guarantees typography does *not* weaken.
//
//   - Idempotency holds unconditionally. Typography is an exception to
//     render preservation, never to this: Format(Format(x)) == Format(x)
//     for every option set, always. This is the check that would catch
//     an ellipsis substitution the sentence segmenter cannot re-read on
//     a second pass (see segment.terminalRun's doc comment).
//   - Render preservation holds modulo the substitutions themselves
//     (normalizeForRender), skipped only for the same narrow, documented
//     shapes fuzz_test.go's hasRenderRiskyShape already gates on — those
//     are orthogonal to typography and apply to every mode equally.
func TestTypographyOverCorpus(t *testing.T) {
	optionSets := []struct {
		name string
		opts mdreflow.Options
	}{
		{"sentence/smart-quotes", mdreflow.Options{Typography: mdreflow.SmartQuotes}},
		{"sentence/ellipses", mdreflow.Options{Typography: mdreflow.Ellipses}},
		{"sentence/both", mdreflow.Options{Typography: mdreflow.SmartQuotes | mdreflow.Ellipses}},
		{"sentence-maxwidth-40/both", mdreflow.Options{MaxWidth: 40, Typography: mdreflow.SmartQuotes | mdreflow.Ellipses}},
		{"para/both", mdreflow.Options{Mode: mdreflow.ModePara, Typography: mdreflow.SmartQuotes | mdreflow.Ellipses}},
		{"wrap-30/both", mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: 30, Typography: mdreflow.SmartQuotes | mdreflow.Ellipses}},
	}

	paths := corpusFixtures(t)
	for _, set := range optionSets {
		t.Run(set.name, func(t *testing.T) {
			for _, path := range paths {
				t.Run(filepath.ToSlash(path), func(t *testing.T) {
					src := mustReadFile(t, path)

					once, err := mdreflow.Format(src, set.opts)
					if err != nil {
						t.Fatalf("Format: %v", err)
					}
					twice, err := mdreflow.Format(once, set.opts)
					if err != nil {
						t.Fatalf("Format(Format(x)): %v", err)
					}
					if !bytes.Equal(twice, once) {
						t.Fatalf("Format is not idempotent with %+v.\n--- once ---\n%s\n--- twice ---\n%s", set.opts, once, twice)
					}

					if hasRenderRiskyShape(src) || hasRenderRiskyShape(once) {
						// Visible, not a bare return: a green run should
						// say when a fixture's render oracle was dark
						// (go-quality review S5).
						t.Skip("render-preservation check skipped: fixture matches a documented risky shape")
					}
					before := normalizeForRender(renderHTML(t, src), set.opts)
					after := normalizeForRender(renderHTML(t, once), set.opts)
					if before != after {
						t.Errorf("rendered HTML changed beyond the typography substitutions.\n--- before ---\n%s\n--- after ---\n%s", before, after)
					}
				})
			}
		})
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mustModeWidth extracts and parses the "w<N>-"/"mw<N>-" width prefix from
// a mode-fixture base name (see modeWidthRE), failing the test if the
// fixture is misnamed.
func mustModeWidth(t *testing.T, base string) int {
	t.Helper()
	m := modeWidthRE.FindStringSubmatch(base)
	if m == nil {
		t.Fatalf("fixture %q has no w<N>-/mw<N>- width prefix in its name", base)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("fixture %q: invalid width prefix: %v", base, err)
	}
	return n
}

// renderHTML renders src with the same goldmark configuration the reflow
// pipeline parses with (internal/gm), so the comparison in
// TestGoldenFixtures/render-preserving is apples to apples.
func renderHTML(t *testing.T, src []byte) string {
	t.Helper()
	md := gm.New()
	doc := md.Parser().Parse(text.NewReader(src))
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

// whitespaceRun matches a run of whitespace, including the literal "\n"
// goldmark's HTML renderer emits for a paragraph's soft line breaks.
var whitespaceRun = regexp.MustCompile(`\s+`)

// spaceBeforeBr matches a single space directly before a "<br>" tag, left
// over after whitespaceRun has already collapsed any longer run to one
// space.
var spaceBeforeBr = regexp.MustCompile(` <br>`)

// anyBrTag matches any spelling of a <br> tag: case-insensitive, optional
// internal spacing, optional self-closing slash — e.g. "<Br>", "<BR />",
// "<br/>". HTML tag names are case-insensitive per the HTML spec, so a
// browser renders all of these identically; goldmark's raw-HTML pass-
// through does not normalize them, so they can differ byte-for-byte
// without differing in rendered meaning. HardBreakStyle normalization
// canonicalizes to "<br>" regardless of which spelling the source used
// (matching design.md's documented hard-break-style render-preservation
// exception), so this rule canonicalizes both sides of a comparison the
// same way — found by FuzzFormat on input "\x00<Br>\n00".
var anyBrTag = regexp.MustCompile(`(?i)<br\s*/?>`)

// normalizeWhitespace collapses whitespace runs to a single space before
// comparing rendered HTML. Reflow moves *where* a paragraph's soft line
// breaks fall without changing that they render as inter-word whitespace
// (a browser collapses "\n" the same as " "), so a literal byte comparison
// of the HTML would flag every reflowed paragraph as a false positive.
//
// It also drops a single leftover space immediately before "<br>". This is
// a second, narrower normalization for a goldmark rendering quirk found by
// FuzzFormat: CommonMark attaches no meaning to more than two trailing
// spaces before a hard break, but goldmark's HTML renderer keeps
// spaces-beyond-two as literal preceding text instead of also collapsing
// them into the break, e.g. "x    \ny" (4 trailing spaces) renders as
// "x <br>\ny", not "x<br>\ny". mdreflow's hard-break detection treats any
// run of 2+ trailing spaces as one break (matching the CommonMark spec's
// stated semantics, and required for HardBreakStyle normalization to have
// one canonical output regardless of how many spaces the source used), so
// it does not reproduce that single leftover space. Dropping it here
// (after normalizeWhitespace, so it also can't reappear from an unrelated
// spelled-out multi-space run elsewhere) treats it as the goldmark
// rendering artifact it is, not a real content difference — a browser
// collapses that one space against the block boundary identically either
// way.
//
// Both normalizations are applied identically to both sides of every
// comparison, so neither can mask a real content change — only the two
// cosmetic differences reflow (and hard-break normalization) are
// explicitly allowed to make.
func normalizeWhitespace(html string) string {
	s := whitespaceRun.ReplaceAllString(html, " ")
	s = anyBrTag.ReplaceAllString(s, "<br>")
	s = spaceBeforeBr.ReplaceAllString(s, "<br>")
	return strings.TrimSpace(s)
}
