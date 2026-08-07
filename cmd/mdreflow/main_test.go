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

	code := run([]string{"--mode=wrap"}, in, &out, &errOut)
	if code != 2 {
		t.Errorf("run(--mode=wrap) exited %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "unsupported --mode") {
		t.Errorf("stderr = %q, want it to mention unsupported --mode", errOut.String())
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
