package mdreflow_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/jbeda/mdreflow/internal/render"

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
		before := normalizeForRender(renderHTML(t, src))
		after := normalizeForRender(renderHTML(t, got))
		if before != after {
			t.Errorf("rendered HTML changed.\n--- before ---\n%s\n--- after ---\n%s", before, after)
		}
	})
}

// normalizeForRender normalizes rendered HTML before comparison. The
// rules were promoted to internal/render when the render backstop made
// them production code; the harness and the backstop now share one
// definition by construction.
func normalizeForRender(html string) string {
	return render.Normalize(html)
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

// TestNeighborMidLineDefShapeReflowsAndConverges covers issue #37 end to
// end: a bullet whose ONLY "[label]:" shape sits mid-prose inside an
// inline code span (a quoted error message, e.g. "runnerGroups[0]: ...")
// no longer freezes the sibling bullet below it, because no definition
// chain can reach a shape with prose to its left. Format twice at a
// narrow wrap width and check both that the previously-frozen second
// bullet now actually reflows and that the result is idempotent.
func TestNeighborMidLineDefShapeReflowsAndConverges(t *testing.T) {
	src := "- See `runnerGroups[0]: priorityClassName is not allowed` here for more context now.\n" +
		"- This continues with more prose and no special characters at all now.\n"
	opts := mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: 20}

	pass1, err := mdreflow.Format([]byte(src), opts)
	if err != nil {
		t.Fatalf("Format (pass1): %v", err)
	}
	pass2, err := mdreflow.Format(pass1, opts)
	if err != nil {
		t.Fatalf("Format (pass2): %v", err)
	}
	if string(pass1) != string(pass2) {
		t.Errorf("Format is not idempotent at MaxWidth 20:\npass1: %q\npass2: %q", pass1, pass2)
	}

	if bytes.Count(pass1, []byte("This continues")) != 1 {
		t.Fatalf("pass1 lost or duplicated the second bullet's opening words: %q", pass1)
	}
	// The second bullet must have actually rewrapped onto more lines than
	// its single source line — proof it left the zone rather than merely
	// surviving pass-through.
	secondBulletStart := bytes.Index(pass1, []byte("This continues"))
	if secondBulletStart < 0 {
		t.Fatalf("second bullet not found in pass1 output: %q", pass1)
	}
	secondBulletLines := bytes.Count(pass1[secondBulletStart:], []byte("\n"))
	if secondBulletLines < 2 {
		t.Errorf("second bullet did not rewrap at MaxWidth 20 (got %d line breaks after it): %q", secondBulletLines, pass1)
	}
}

// corpusFixtures returns every fixture input in testdata/ — the top-level
// family and the mode families — as paths.
func corpusFixtures(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, pattern := range []string{
		filepath.Join("testdata", "*.md"),
		filepath.Join("testdata", "modes", "*", "*.md"),
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

// TestPropertyOverCorpus is the fixtures-by-options property loop
// docs/design.md's Testing section item 2 calls for: every fixture in
// the repository (goldens included, since a golden is itself a valid
// input) is formatted under a spread of mode/width option sets, and must
// be idempotent and render-preserving under each.
func TestPropertyOverCorpus(t *testing.T) {
	optionSets := []struct {
		name string
		opts mdreflow.Options
	}{
		{"sentence", mdreflow.Options{}},
		{"sentence-maxwidth-40", mdreflow.Options{MaxWidth: 40}},
		{"para", mdreflow.Options{Mode: mdreflow.ModePara}},
		{"wrap-30", mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: 30}},
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
					before := normalizeForRender(renderHTML(t, src))
					after := normalizeForRender(renderHTML(t, once))
					if before != after {
						t.Errorf("rendered HTML changed.\n--- before ---\n%s\n--- after ---\n%s", before, after)
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
	h, err := render.HTML(src)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return h
}

// TestRenderBackstopNeverTripsOnCorpus asserts the render backstop is a
// no-op on every legitimate document: for every fixture under every
// option spread, the public Format (backstop enabled) must return the
// same bytes as the raw pipeline (backstop disabled). A difference means
// the backstop silently suppressed a reflow — the false-fallback bug
// class the design doc names as this feature's own product risk (an
// over-strict normalization would make ordinary documents mysteriously
// stop reflowing, visible only as a diff nobody gets).
func TestRenderBackstopNeverTripsOnCorpus(t *testing.T) {
	optionSets := []struct {
		name string
		opts mdreflow.Options
	}{
		{"sentence", mdreflow.Options{}},
		{"sentence-maxwidth-40", mdreflow.Options{MaxWidth: 40}},
		{"para", mdreflow.Options{Mode: mdreflow.ModePara}},
		{"wrap-30", mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: 30}},
	}
	for _, set := range optionSets {
		t.Run(set.name, func(t *testing.T) {
			for _, path := range corpusFixtures(t) {
				t.Run(filepath.ToSlash(path), func(t *testing.T) {
					src := mustReadFile(t, path)

					withBackstop, err := mdreflow.Format(src, set.opts)
					if err != nil {
						t.Fatalf("Format: %v", err)
					}
					mdreflow.SetRenderBackstop(false)
					raw, err := mdreflow.Format(src, set.opts)
					mdreflow.SetRenderBackstop(true)
					if err != nil {
						t.Fatalf("Format (backstop off): %v", err)
					}
					if !bytes.Equal(withBackstop, raw) {
						t.Errorf("render backstop suppressed a reflow.\n--- raw pipeline ---\n%s\n--- with backstop ---\n%s", raw, withBackstop)
					}
				})
			}
		})
	}
}
