package mdreflow_test

import (
	"strings"
	"testing"

	"github.com/jbeda/mdreflow"
)

// TestFormatRejectsUnimplemented checks that every still-unimplemented or
// invalid option combination fails loudly, per Format's doc comment,
// instead of silently ignoring the option or producing wrong output.
func TestFormatRejectsUnimplemented(t *testing.T) {
	cases := []struct {
		name string
		opts mdreflow.Options
	}{
		{"para mode with max width", mdreflow.Options{Mode: mdreflow.ModePara, MaxWidth: 80}},
		{"typography", mdreflow.Options{Typography: mdreflow.SmartQuotes}},
		{"unknown mode", mdreflow.Options{Mode: mdreflow.Mode(99)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := mdreflow.Format([]byte("Some text.\n"), tc.opts)
			if err == nil {
				t.Fatalf("Format with %s: want error, got nil", tc.name)
			}
		})
	}
}

// TestFormatAcceptsModesAndMaxWidth checks that para mode, wrap mode, and
// sentence-mode MaxWidth — all implemented in M3 — no longer error.
func TestFormatAcceptsModesAndMaxWidth(t *testing.T) {
	cases := []struct {
		name string
		opts mdreflow.Options
	}{
		{"para mode", mdreflow.Options{Mode: mdreflow.ModePara}},
		{"wrap mode", mdreflow.Options{Mode: mdreflow.ModeWrap}},
		{"wrap mode zero width", mdreflow.Options{Mode: mdreflow.ModeWrap, MaxWidth: 0}},
		{"sentence mode max width", mdreflow.Options{MaxWidth: 40}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := mdreflow.Format([]byte("Some text.\n"), tc.opts)
			if err != nil {
				t.Fatalf("Format with %s: %v", tc.name, err)
			}
		})
	}
}

// TestFormatZeroOptions checks the documented promise that Options{} (the
// zero value) is valid and behaves as sentence mode.
func TestFormatZeroOptions(t *testing.T) {
	got, err := mdreflow.Format([]byte("One. Two.\n"), mdreflow.Options{})
	if err != nil {
		t.Fatalf("Format(Options{}): %v", err)
	}
	if string(got) != "One.\nTwo.\n" {
		t.Errorf("Format(Options{}) = %q, want %q", got, "One.\nTwo.\n")
	}
}

func TestCheck(t *testing.T) {
	unformatted := []byte("One. Two.\n")
	changed, err := mdreflow.Check(unformatted, mdreflow.Options{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !changed {
		t.Error("Check(unformatted) = false, want true")
	}

	formatted := []byte("One.\nTwo.\n")
	changed, err = mdreflow.Check(formatted, mdreflow.Options{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if changed {
		t.Error("Check(formatted) = true, want false")
	}
}

func TestFormatReader(t *testing.T) {
	var out strings.Builder
	err := mdreflow.FormatReader(&out, strings.NewReader("One. Two.\n"), mdreflow.Options{})
	if err != nil {
		t.Fatalf("FormatReader: %v", err)
	}
	if out.String() != "One.\nTwo.\n" {
		t.Errorf("FormatReader wrote %q, want %q", out.String(), "One.\nTwo.\n")
	}
}

// TestStripSentenceTerminalBreaks checks that the option removes a
// trailing double-space hard break only when it immediately follows
// sentence-terminal punctuation, and leaves other hard breaks alone.
func TestStripSentenceTerminalBreaks(t *testing.T) {
	// Paragraph 1: the double-space break directly after "here." (sentence-
	// terminal punctuation) is accidental and gets stripped; the ordinary
	// segmenter still finds and re-inserts the same sentence boundary, just
	// without a hard-break marker.
	//
	// Paragraph 2: the double-space break after "word" (no terminal
	// punctuation) is a deliberate hard break and is kept, normalized to
	// the default <br> style.
	src := []byte("First sentence ends here.  \nSecond line continues normally.\n\nEnds with word  \nNext line.\n")
	got, err := mdreflow.Format(src, mdreflow.Options{StripSentenceTerminalBreaks: true})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	want := "First sentence ends here.\nSecond line continues normally.\n\nEnds with word<br>\nNext line.\n"
	if string(got) != want {
		t.Errorf("Format with StripSentenceTerminalBreaks = %q, want %q", got, want)
	}
}

func TestDefaultAbbreviationsIncludesCoreSet(t *testing.T) {
	got := mdreflow.DefaultAbbreviations()
	want := map[string]bool{"Mr.": false, "Dr.": false, "etc.": false, "e.g.": false, "U.S.": false}
	for _, a := range got {
		if _, ok := want[a]; ok {
			want[a] = true
		}
	}
	for a, found := range want {
		if !found {
			t.Errorf("DefaultAbbreviations() missing expected entry %q", a)
		}
	}
}

// TestCustomSegmenter checks that Options.Segmenter fully overrides the
// built-in segmenter (and that Options.Abbreviations is ignored in that
// case, per its doc comment).
type allCapsSegmenter struct{}

func (allCapsSegmenter) Breaks(text string) []mdreflow.Span {
	// A deliberately silly segmenter: break after every "X".
	var out []mdreflow.Span
	for i := 0; i < len(text); i++ {
		if text[i] == 'X' && i+1 < len(text) && text[i+1] == ' ' {
			out = append(out, mdreflow.Span{Start: i + 1, End: i + 2})
		}
	}
	return out
}

func TestCustomSegmenter(t *testing.T) {
	got, err := mdreflow.Format([]byte("aX bX c\n"), mdreflow.Options{Segmenter: allCapsSegmenter{}})
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	want := "aX\nbX\nc\n"
	if string(got) != want {
		t.Errorf("Format with custom Segmenter = %q, want %q", got, want)
	}
}
