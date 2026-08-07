package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRunSentenceMode is a smoke test for the stdin-to-stdout CLI path —
// the M1 CLI's entire surface area.
func TestRunSentenceMode(t *testing.T) {
	in := strings.NewReader("One sentence. Two sentence.\n")
	var out, errOut bytes.Buffer

	code := run(nil, in, &out, &errOut)
	if code != 0 {
		t.Fatalf("run exited %d, stderr=%q", code, errOut.String())
	}
	want := "One sentence.\nTwo sentence.\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}

func TestRunExplicitSentenceMode(t *testing.T) {
	in := strings.NewReader("Already fine.\n")
	var out, errOut bytes.Buffer

	code := run([]string{"--mode=sentence"}, in, &out, &errOut)
	if code != 0 {
		t.Fatalf("run exited %d, stderr=%q", code, errOut.String())
	}
	if out.String() != "Already fine.\n" {
		t.Errorf("stdout = %q, want %q", out.String(), "Already fine.\n")
	}
}

func TestRunRejectsUnknownMode(t *testing.T) {
	in := strings.NewReader("text\n")
	var out, errOut bytes.Buffer

	code := run([]string{"--mode=bogus"}, in, &out, &errOut)
	if code != 2 {
		t.Errorf("run(--mode=bogus) exited %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unsupported --mode") {
		t.Errorf("stderr = %q, want it to mention unsupported --mode", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty on error", out.String())
	}
}

func TestRunParaMode(t *testing.T) {
	in := strings.NewReader("One sentence. Two sentence.\n")
	var out, errOut bytes.Buffer

	code := run([]string{"--mode=para"}, in, &out, &errOut)
	if code != 0 {
		t.Fatalf("run exited %d, stderr=%q", code, errOut.String())
	}
	want := "One sentence. Two sentence.\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}

func TestRunWrapMode(t *testing.T) {
	in := strings.NewReader("One two three four five six seven eight nine ten.\n")
	var out, errOut bytes.Buffer

	code := run([]string{"--mode=wrap", "--max-width=10"}, in, &out, &errOut)
	if code != 0 {
		t.Fatalf("run exited %d, stderr=%q", code, errOut.String())
	}
	want := "One two\nthree four\nfive six\nseven\neight nine\nten.\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}

func TestRunWrapModeDefaultWidth(t *testing.T) {
	in := strings.NewReader("word " + strings.Repeat("x", 90) + "\n")
	var out, errOut bytes.Buffer

	code := run([]string{"--mode=wrap"}, in, &out, &errOut)
	if code != 0 {
		t.Fatalf("run exited %d, stderr=%q", code, errOut.String())
	}
	// With --max-width omitted (0), wrap mode defaults to 80: "word" plus
	// the 90-x run does not fit on one line together, so it splits.
	want := "word\n" + strings.Repeat("x", 90) + "\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
}

func TestRunRejectsMaxWidthWithParaMode(t *testing.T) {
	in := strings.NewReader("text\n")
	var out, errOut bytes.Buffer

	code := run([]string{"--mode=para", "--max-width=40"}, in, &out, &errOut)
	if code != 2 {
		t.Errorf("run(--mode=para --max-width=40) exited %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "--max-width") {
		t.Errorf("stderr = %q, want it to mention --max-width", errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty on error", out.String())
	}
}

func TestRunRejectsUnknownFlag(t *testing.T) {
	in := strings.NewReader("text\n")
	var out, errOut bytes.Buffer

	code := run([]string{"--bogus"}, in, &out, &errOut)
	if code != 2 {
		t.Errorf("run(--bogus) exited %d, want 2", code)
	}
}
