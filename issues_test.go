package mdreflow_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/jbeda/mdreflow"
)

// TestInvalidUTF8Refused pins the input-domain guarantee (design.md,
// Guarantees): invalid UTF-8 gets a typed refusal from every entry point,
// with no partial output.
func TestInvalidUTF8Refused(t *testing.T) {
	bad := []byte("ok so far \xa0\xa0 not utf-8")

	if out, err := mdreflow.Format(bad, mdreflow.Options{}); !errors.Is(err, mdreflow.ErrInvalidUTF8) {
		t.Errorf("Format: got (%q, %v), want ErrInvalidUTF8", out, err)
	}
	if _, err := mdreflow.NeedsFormat(bad, mdreflow.Options{}); !errors.Is(err, mdreflow.ErrInvalidUTF8) {
		t.Errorf("Check: got %v, want ErrInvalidUTF8", err)
	}
	var buf bytes.Buffer
	if err := mdreflow.FormatReader(&buf, bytes.NewReader(bad), mdreflow.Options{}); !errors.Is(err, mdreflow.ErrInvalidUTF8) {
		t.Errorf("FormatReader: got %v, want ErrInvalidUTF8", err)
	}
	if buf.Len() != 0 {
		t.Errorf("FormatReader wrote %d bytes despite refusal", buf.Len())
	}
}

// TestConvergenceBackstop pins the user-facing guarantee that Format's
// output is a fixpoint even on inputs whose single-pass reflow is not yet
// idempotent (the TestFuzzFamilyRegressions corners): formatting Format's
// output must return it unchanged.
func TestConvergenceBackstop(t *testing.T) {
	// Issue #11's repro: single-pass non-idempotent until root-fixed.
	src := []byte("[\\]\n]:0\n\"\"0")
	opts := mdreflow.Options{Mode: mdreflow.ModePara}

	out, err := mdreflow.Format(src, opts)
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	again, err := mdreflow.Format(out, opts)
	if err != nil {
		t.Fatalf("Format(Format(x)): %v", err)
	}
	if !bytes.Equal(out, again) {
		t.Errorf("public Format output is not a fixpoint.\nout:   %q\nagain: %q", out, again)
	}
}

// TestFuzzFamilyRegressions pins the fuzz-found idempotency and
// render-preservation corners tracked as issues #5-#8 and #10-#13 (see
// each issue for the mechanism analysis). Inputs are the issues' repros,
// re-encoded as valid UTF-8 where the original raw bytes were not (#10:
// the 0xa0 padding was incidental; the ASCII variant reproduces the same
// segmenter double-space flip).
//
// Issue #4 has no recoverable exact repro (its input predates seed
// retention); it shares #11's link-reference-definition mechanism and is
// covered by that fix plus fuzz soaks.
func TestFuzzFamilyRegressions(t *testing.T) {
	cases := []struct {
		name        string
		src         string
		opts        mdreflow.Options
		checkRender bool // #5, #13: render preservation, not just idempotency
	}{
		{
			name: "issue5-sentence-maxwidth-fresh-table",
			src:  "\v -|-|-|-|-|-|-",
			opts: mdreflow.Options{Mode: mdreflow.ModeSentence, MaxWidth: 14},

			checkRender: true,
		},
		{
			name: "issue6-issue12-hardbreak-fence-escape-repair",
			src:  "`  \n    ```",
			opts: mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: 13},
		},
		{
			// No longer reproduces as of the wrapRanked rewrite (0c8dc31):
			// the dash run is now escaped and the result is idempotent
			// across the whole option matrix. Kept as a pin.
			name: "issue7-list-item-narrow-wrap-rejoin",
			src:  "00\n- -- 000+\n*",
			opts: mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: 6},
		},
		{
			// Originally fuzz-found with typography's SmartQuotes flag
			// (since removed) exposing the pairing flip; the input keeps
			// pinning the fence-escape/code-span-pairing shape itself.
			name: "issue8-fence-escape-codespan-pairing",
			src:  "\r~~~``'``1100X00",
			opts: mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: 25},
		},
		{
			name: "issue10-sentence-boundary-double-space-consumed",
			src:  "padpadpad \rA.  BC 2.20",
			opts: mdreflow.Options{MaxWidth: 8},
		},
		{
			name: "issue11-para-linkrefdef-skip-flip",
			src:  "[\\]\n]:0\n\"\"0",
			opts: mdreflow.Options{Mode: mdreflow.ModePara},
		},
		{
			// The issue says "default sentence mode" but the split only
			// happens under width pressure; MaxWidth 1 (any small width)
			// reproduces the table-creating line-start split.
			name: "issue13-sentence-split-table-delimiter-line-start",
			src:  "\f -:\n\n00\n0",
			opts: mdreflow.Options{MaxWidth: 1},

			checkRender: true,
		},
		{
			// A tab-indented list-item body of "**"/"**" reflows to the
			// single line "** **" — four asterisks, a thematic break — and
			// the tab in its firstLinePrefix hid that from the joint
			// thematic-break escape (which counted only spaces as indent).
			// The break silently ended the list and dropped the following
			// ::: paragraph out of it, a render change; sentence mode splits
			// the body into its own cluster on the ::: boundary.
			name: "issue32-tab-indented-join-thematic-break",
			src:  "*\n\t**\n**\n:::\n0",
			opts: mdreflow.Options{Mode: mdreflow.ModeSentence},

			checkRender: true,
		},
	}

	// These are single-pass regressions: the convergence backstop would
	// mask the idempotency ones without fixing anything. Root fixes only.
	mdreflow.SetConvergenceBackstop(false)
	defer mdreflow.SetConvergenceBackstop(true)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			once, err := mdreflow.Format([]byte(tc.src), tc.opts)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			twice, err := mdreflow.Format(once, tc.opts)
			if err != nil {
				t.Fatalf("Format(Format(x)): %v", err)
			}
			if !bytes.Equal(once, twice) {
				t.Errorf("not idempotent.\nsrc:   %q\nonce:  %q\ntwice: %q", tc.src, once, twice)
			}
			if tc.checkRender {
				before := normalizeForRender(renderHTML(t, []byte(tc.src)))
				after := normalizeForRender(renderHTML(t, once))
				if before != after {
					t.Errorf("rendered HTML changed.\nsrc: %q\nout: %q\n--- before ---\n%s\n--- after ---\n%s", tc.src, once, before, after)
				}
			}
		})
	}
}
