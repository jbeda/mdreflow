package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadOrdinaryConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	writeFile(t, p, "mode: wrap\nmax-width: 80\n")

	f, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if f.Mode != "wrap" || f.MaxWidth != 80 {
		t.Errorf("Load(%q) = %+v, want Mode=wrap MaxWidth=80", p, f)
	}
}

// TestLoadRefusesOversizedFile pins security review S2: a .mdreflow.yaml
// bigger than MaxFileSize is refused before it is read into memory.
func TestLoadRefusesOversizedFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	if err := os.WriteFile(p, make([]byte, MaxFileSize+1), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(p)
	if err == nil {
		t.Fatal("Load of an oversized config file: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error = %q, want it to mention the file being too large", err)
	}
}

// TestLoadRefusesAliasBomb reproduces security review S2's alias-bomb
// payload shape: nested anchors whose fan-out compounds exponentially.
// The review's own 10-anchor, 9-way payload produces only 82 literal
// '*name' tokens (the blowup is in expansion, not source size), which
// is below a naive 100-token threshold — see maxAliasRefs's doc comment
// — so this uses an 11-way fan-out (110 tokens) to comfortably clear the
// limit this package actually enforces while keeping the same shape and
// the same order-of-magnitude expansion (11^10 scalars, still
// astronomically more than any real config needs). Must fail fast
// (bounded by the test's own timeout) rather than hang or OOM.
func TestLoadRefusesAliasBomb(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)

	const fanOut = 11 // see doc comment: 9 (the review's own PoC) stays under maxAliasRefs
	var b strings.Builder
	fmt.Fprintf(&b, "a0: &a0 [%s]\n", strings.TrimSuffix(strings.Repeat(`"x",`, fanOut), ","))
	for i := 1; i <= 10; i++ {
		refs := strings.TrimSuffix(strings.Repeat(fmt.Sprintf("*a%d,", i-1), fanOut), ",")
		fmt.Fprintf(&b, "a%d: &a%d [%s]\n", i, i, refs)
	}
	b.WriteString("exclude: [*a10]\n")
	writeFile(t, p, b.String())

	done := make(chan struct{})
	var err error
	go func() {
		_, err = Load(p)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Load did not return within 5s; alias bomb was not rejected before parsing")
	}
	if err == nil {
		t.Fatal("Load of an alias bomb: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "alias") {
		t.Errorf("error = %q, want it to mention alias references", err)
	}
}

// TestLoadRefusesDeepNesting reproduces security review S2's second
// payload shape: "mode: " followed by 100000 "[" characters, a
// parse-time bomb that never reaches the struct (so Strict()'s
// unknown-key check can't help). Must fail fast rather than hang.
func TestLoadRefusesDeepNesting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	writeFile(t, p, "mode: "+strings.Repeat("[", 100000))

	done := make(chan struct{})
	var err error
	go func() {
		_, err = Load(p)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Load did not return within 5s; deep-nesting bomb was not rejected before parsing")
	}
	if err == nil {
		t.Fatal("Load of a deeply-nested config: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "nesting depth") {
		t.Errorf("error = %q, want it to mention nesting depth", err)
	}
}

// TestLoadDoesNotFlagOrdinaryGlobExcludes guards against the alias-
// reference pre-scan over-triggering on ordinary exclude: glob patterns,
// which are '*'-heavy but never at an alias-reference boundary.
func TestLoadDoesNotFlagOrdinaryGlobExcludes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	var b strings.Builder
	b.WriteString("exclude:\n")
	for i := 0; i < 60; i++ {
		b.WriteString("  - \"generated/**/*.md\"\n")
	}
	writeFile(t, p, b.String())

	f, err := Load(p)
	if err != nil {
		t.Fatalf("Load of an ordinary glob-heavy config: %v", err)
	}
	if len(f.Exclude) != 60 {
		t.Errorf("len(Exclude) = %d, want 60", len(f.Exclude))
	}
}

func TestDiscoverStopsAtBoundary(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	boundary := filepath.Join(root, "boundary")
	inside := filepath.Join(boundary, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(outside, FileName), "mode: wrap\n")

	found, err := Discover(inside, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if found != "" {
		t.Errorf("Discover(%q, %q) = %q, want \"\": a config outside the boundary must not be found", inside, boundary, found)
	}
}

func TestDiscoverFindsConfigAtBoundaryItself(t *testing.T) {
	root := t.TempDir()
	boundary := filepath.Join(root, "boundary")
	inside := filepath.Join(boundary, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(boundary, FileName)
	writeFile(t, cfgPath, "mode: wrap\n")

	found, err := Discover(inside, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if found != cfgPath {
		t.Errorf("Discover(%q, %q) = %q, want %q: a config at the boundary itself still applies", inside, boundary, found, cfgPath)
	}
}

func TestDiscoverFindsConfigBelowBoundary(t *testing.T) {
	root := t.TempDir()
	boundary := filepath.Join(root, "boundary")
	inside := filepath.Join(boundary, "inside")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(inside, FileName)
	writeFile(t, cfgPath, "mode: wrap\n")

	found, err := Discover(inside, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if found != cfgPath {
		t.Errorf("Discover(%q, %q) = %q, want %q", inside, boundary, found, cfgPath)
	}
}

func TestDiscoverEmptyBoundaryIsUnbounded(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, FileName)
	writeFile(t, cfgPath, "mode: wrap\n")

	found, err := Discover(deep, "")
	if err != nil {
		t.Fatal(err)
	}
	if found != cfgPath {
		t.Errorf("Discover(%q, \"\") = %q, want %q (unbounded fallback)", deep, found, cfgPath)
	}
}
