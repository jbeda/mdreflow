package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI is the harness every M4 integration test uses: it calls run()
// directly (no subprocess) so failures show Go stack traces and the
// race detector still works, per docs/design.md's Testing section
// ("running the real binary logic via a testable run() function").
func runCLI(t *testing.T, args []string, stdin string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const unformatted = "One sentence. Two sentence.\n"
const formatted = "One sentence.\nTwo sentence.\n"

// --- in-place writes ---

func TestRunInPlaceReformatsAndExitsClean(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, unformatted)

	_, errOut, code := runCLI(t, []string{p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	if got := readFile(t, p); got != formatted {
		t.Errorf("file content = %q, want %q", got, formatted)
	}
}

func TestRunInPlaceCleanFileUntouched(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, formatted)

	_, _, code := runCLI(t, []string{p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d", code, exitOK)
	}
	if got := readFile(t, p); got != formatted {
		t.Errorf("file content = %q, want unchanged %q", got, formatted)
	}
}

// --- --check / --diff ---

func TestRunCheckReportsAndExitsOne(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, unformatted)

	out, _, code := runCLI(t, []string{"--check", p}, "")
	if code != exitWouldReformat {
		t.Fatalf("exit=%d, want %d", code, exitWouldReformat)
	}
	if !strings.Contains(out, "would reformat") || !strings.Contains(out, p) {
		t.Errorf("stdout = %q, want a one-line report naming %s", out, p)
	}
	if got := readFile(t, p); got != unformatted {
		t.Errorf("--check must not write; file = %q", got)
	}
}

func TestRunCheckCleanExitsZero(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, formatted)

	out, _, code := runCLI(t, []string{"--check", p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d", code, exitOK)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty when nothing would change", out)
	}
}

func TestRunDiffOutputsUnifiedDiffAndExitsOne(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, unformatted)

	out, _, code := runCLI(t, []string{"--diff", p}, "")
	if code != exitWouldReformat {
		t.Fatalf("exit=%d, want %d", code, exitWouldReformat)
	}
	if !strings.Contains(out, "--- "+p) || !strings.Contains(out, "+++ "+p) {
		t.Errorf("stdout = %q, want a unified diff header for %s", out, p)
	}
	if !strings.Contains(out, "-"+unformatted[:len(unformatted)-1]) {
		t.Errorf("stdout = %q, want a removed-line marker for the original content", out)
	}
	if got := readFile(t, p); got != unformatted {
		t.Errorf("--diff must not write; file = %q", got)
	}
}

func TestRunStdinCheck(t *testing.T) {
	out, _, code := runCLI(t, []string{"--check"}, unformatted)
	if code != exitWouldReformat {
		t.Fatalf("exit=%d, want %d", code, exitWouldReformat)
	}
	if !strings.Contains(out, "would reformat -") {
		t.Errorf("stdout = %q, want it to report stdin as -", out)
	}
}

func TestRunStdinDiff(t *testing.T) {
	out, _, code := runCLI(t, []string{"--diff"}, unformatted)
	if code != exitWouldReformat {
		t.Fatalf("exit=%d, want %d", code, exitWouldReformat)
	}
	if !strings.Contains(out, "--- -") || !strings.Contains(out, "+++ -") {
		t.Errorf("stdout = %q, want a unified diff labeled -", out)
	}
}

// --- usage errors ---

func TestRunUsageErrorBadFlagCombo(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, unformatted)

	_, errOut, code := runCLI(t, []string{"--mode=para", "--max-width=40", p}, "")
	if code != exitUsage {
		t.Fatalf("exit=%d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "--max-width") {
		t.Errorf("stderr = %q, want it to mention --max-width", errOut)
	}
}

func TestRunUsageErrorMissingPath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.md")

	_, _, code := runCLI(t, []string{missing}, "")
	if code != exitUsage {
		t.Fatalf("exit=%d, want %d", code, exitUsage)
	}
}

// --- config discovery and precedence ---

func TestRunConfigDrivesFormattingWhenNoFlagsGiven(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mdreflow.yaml"), "mode: wrap\nmax-width: 20\n")
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, "one two three four five six seven eight nine ten\n")

	_, errOut, code := runCLI(t, []string{p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	got := readFile(t, p)
	if !strings.Contains(got, "\n") || strings.Count(got, "\n") < 2 {
		t.Errorf("expected config-driven wrap at width 20 to split into multiple lines, got %q", got)
	}
}

// Non-zero widths below mdreflow.MinMaxWidth are a usage error (exit 2):
// very narrow widths force breaks inside Markdown constructs and were the
// source of most fuzz-found width pathology (docs/design.md, "The width
// floor").
func TestRunMaxWidthBelowFloorIsUsageError(t *testing.T) {
	_, errOut, code := runCLI(t, []string{"--mode=wrap", "--max-width=10"}, "Some text.\n")
	if code != exitUsage {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "--max-width") || !strings.Contains(errOut, "20") {
		t.Errorf("stderr = %q, want it to name --max-width and the floor", errOut)
	}
}

func TestRunFlagBeatsConfigEvenAtZeroValue(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mdreflow.yaml"), "mode: wrap\nmax-width: 20\n")
	p := filepath.Join(dir, "a.md")
	content := "one two three four five six seven eight nine ten\n"
	writeFile(t, p, content)

	// --max-width=0 is the flag's zero value (same as never passing it),
	// but explicitly setting it must still override the config's
	// max-width: 20 — wrap mode's 0 means "default to 80", which is wide
	// enough that this line does not need to wrap at all.
	_, errOut, code := runCLI(t, []string{"--max-width=0", p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	if got := readFile(t, p); got != content {
		t.Errorf("explicit --max-width=0 should have overridden config's max-width: 20; got %q", got)
	}
}

func TestRunConfigUnknownKeyIsUsageError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mdreflow.yaml"), "mode: sentence\nbogus-key: true\n")
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, "Hi.\n")

	_, errOut, code := runCLI(t, []string{p}, "")
	if code != exitUsage {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "bogus-key") {
		t.Errorf("stderr = %q, want it to name the unknown key", errOut)
	}
}

func TestRunConfigInvalidModeIsUsageError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mdreflow.yaml"), "mode: sideways\n")
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, "Hi.\n")

	_, errOut, code := runCLI(t, []string{p}, "")
	if code != exitUsage {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "sideways") {
		t.Errorf("stderr = %q, want it to name the bad mode value", errOut)
	}
}

// Typography was removed (docs/design.md, "Typography: removed"); a
// leftover typography: key in a discovered config must be a loud config
// error, never a silent no-op.
func TestRunConfigTypographyKeyIsLoudError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mdreflow.yaml"), "typography: [smart-quotes]\n")
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, "Hi.\n")

	_, errOut, code := runCLI(t, []string{p}, "")
	if code != exitUsage {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "typography") {
		t.Errorf("stderr = %q, want it to name the unknown typography key", errOut)
	}
}

func TestRunExplicitConfigFlag(t *testing.T) {
	dir := t.TempDir()
	// Config lives somewhere the file isn't nested under, so upward
	// discovery from the file would never find it.
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "custom.yaml")
	writeFile(t, cfgPath, "mode: para\n")

	p := filepath.Join(dir, "a.md")
	writeFile(t, p, unformatted)

	_, errOut, code := runCLI(t, []string{"--config", cfgPath, p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	if got := readFile(t, p); got != unformatted {
		t.Errorf("para mode joins to one line and the input is already one line, so nothing should change; got %q", got)
	}
}

// --- excludes ---

func TestRunExcludeGitignoreRespectedByDefault(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	mustGitInit(t, dir)
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.md\n")
	writeFile(t, filepath.Join(dir, "ignored.md"), unformatted)
	writeFile(t, filepath.Join(dir, "keep.md"), unformatted)

	_, _, code := runCLI(t, []string{dir}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d", code, exitOK)
	}
	if got := readFile(t, filepath.Join(dir, "ignored.md")); got != unformatted {
		t.Errorf("ignored.md should not have been touched, got %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "keep.md")); got != formatted {
		t.Errorf("keep.md should have been reformatted, got %q", got)
	}
}

func TestRunNoGitignoreFlagDisablesIt(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	mustGitInit(t, dir)
	writeFile(t, filepath.Join(dir, ".gitignore"), "ignored.md\n")
	writeFile(t, filepath.Join(dir, "ignored.md"), unformatted)

	_, _, code := runCLI(t, []string{"--no-gitignore", dir}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d", code, exitOK)
	}
	if got := readFile(t, filepath.Join(dir, "ignored.md")); got != formatted {
		t.Errorf("--no-gitignore should let ignored.md be reformatted, got %q", got)
	}
}

func TestRunConfigExcludePatterns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mdreflow.yaml"), "exclude:\n  - \"skip.md\"\n")
	writeFile(t, filepath.Join(dir, "skip.md"), unformatted)
	writeFile(t, filepath.Join(dir, "keep.md"), unformatted)

	_, _, code := runCLI(t, []string{dir}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d", code, exitOK)
	}
	if got := readFile(t, filepath.Join(dir, "skip.md")); got != unformatted {
		t.Errorf("skip.md should not have been touched, got %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "keep.md")); got != formatted {
		t.Errorf("keep.md should have been reformatted, got %q", got)
	}
}

func TestRunExplicitExcludedFileRefused(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mdreflow.yaml"), "exclude:\n  - \"skip.md\"\n")
	p := filepath.Join(dir, "skip.md")
	writeFile(t, p, unformatted)

	_, errOut, code := runCLI(t, []string{p}, "")
	if code != exitRefused {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitRefused, errOut)
	}
	if !strings.Contains(errOut, "skipped (excluded by config)") {
		t.Errorf("stderr = %q, want a skipped-by-config message", errOut)
	}
	if got := readFile(t, p); got != unformatted {
		t.Errorf("refused file must not be written, got %q", got)
	}
}

func TestRunExplicitExcludedFileForced(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mdreflow.yaml"), "exclude:\n  - \"skip.md\"\n")
	p := filepath.Join(dir, "skip.md")
	writeFile(t, p, unformatted)

	_, errOut, code := runCLI(t, []string{"--force", p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	if got := readFile(t, p); got != formatted {
		t.Errorf("--force should have formatted the file despite the exclude, got %q", got)
	}
}

// --- non-Markdown input detection ---

func TestRunExplicitNonMarkdownExtensionRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data.yaml")
	writeFile(t, p, "key: value\n")

	_, errOut, code := runCLI(t, []string{p}, "")
	if code != exitRefused {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitRefused, errOut)
	}
	if !strings.Contains(errOut, "not a Markdown file") {
		t.Errorf("stderr = %q, want a not-a-Markdown-file message", errOut)
	}
}

func TestRunExplicitNonMarkdownExtensionForced(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data.yaml")
	writeFile(t, p, "key: value\n")

	_, errOut, code := runCLI(t, []string{"--force", p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
}

func TestRunBinaryContentRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "binary.md")
	if err := os.WriteFile(p, []byte("hello\x00world"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, errOut, code := runCLI(t, []string{p}, "")
	if code != exitRefused {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitRefused, errOut)
	}
	if !strings.Contains(errOut, "binary content") {
		t.Errorf("stderr = %q, want a binary-content message", errOut)
	}
}

func TestRunInvalidUTF8Refused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.md")
	if err := os.WriteFile(p, []byte("hello \xff\xfe world"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, errOut, code := runCLI(t, []string{p}, "")
	if code != exitRefused {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitRefused, errOut)
	}
	if !strings.Contains(errOut, "UTF-8") {
		t.Errorf("stderr = %q, want an invalid-UTF-8 message", errOut)
	}
}

// --- directory walk extension filtering ---

func TestRunDirectoryWalkOnlyPicksUpMarkdownExtensions(t *testing.T) {
	dir := t.TempDir()
	md := []string{"a.md", "b.mdx", "c.markdown"}
	other := []string{"d.txt", "e.json"}
	for _, f := range md {
		writeFile(t, filepath.Join(dir, f), unformatted)
	}
	for _, f := range other {
		writeFile(t, filepath.Join(dir, f), unformatted)
	}

	_, _, code := runCLI(t, []string{dir}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d", code, exitOK)
	}
	for _, f := range md {
		if got := readFile(t, filepath.Join(dir, f)); got != formatted {
			t.Errorf("%s should have been reformatted, got %q", f, got)
		}
	}
	for _, f := range other {
		if got := readFile(t, filepath.Join(dir, f)); got != unformatted {
			t.Errorf("%s should not have been touched, got %q", f, got)
		}
	}
}

// --- exit code precedence ---

func TestRunExitCodePrecedenceRefusalOverReformat(t *testing.T) {
	dir := t.TempDir()
	needsReformat := filepath.Join(dir, "good.md")
	writeFile(t, needsReformat, unformatted)
	refused := filepath.Join(dir, "bad.yaml")
	writeFile(t, refused, "key: value\n")

	_, errOut, code := runCLI(t, []string{"--check", needsReformat, refused}, "")
	if code != exitRefused {
		t.Fatalf("exit=%d, want %d (refusal outranks would-reformat); stderr=%q", code, exitRefused, errOut)
	}
	if !strings.Contains(errOut, "not a Markdown file") {
		t.Errorf("stderr = %q, want the refusal to still be reported", errOut)
	}
}

// --- --stdout ---

func TestRunStdoutFlagSingleFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, unformatted)

	out, _, code := runCLI(t, []string{"--stdout", p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d", code, exitOK)
	}
	if out != formatted {
		t.Errorf("stdout = %q, want %q", out, formatted)
	}
	if got := readFile(t, p); got != unformatted {
		t.Errorf("--stdout must not write in place, got %q", got)
	}
}

func TestRunStdoutFlagMultipleFilesIsUsageError(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.md")
	writeFile(t, a, unformatted)
	b := filepath.Join(dir, "b.md")
	writeFile(t, b, unformatted)

	_, errOut, code := runCLI(t, []string{"--stdout", a, b}, "")
	if code != exitUsage {
		t.Fatalf("exit=%d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "--stdout") {
		t.Errorf("stderr = %q, want it to mention --stdout", errOut)
	}
}

// --- hard-breaks / strip-sentence-terminal-breaks flags ---

func TestRunHardBreaksFlag(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	// Two lines joined by a hard break (trailing double space): stays
	// two lines, but the hard-break spelling is normalized.
	writeFile(t, p, "Line one.  \nLine two.\n")

	_, errOut, code := runCLI(t, []string{"--hard-breaks=backslash", p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	got := readFile(t, p)
	if !strings.Contains(got, "\\\n") {
		t.Errorf("expected a backslash hard break in output, got %q", got)
	}
}

// --help is not an error: full text on stdout, exit 0 (agents branch on
// this), mentioning every flag, the exit-code contract, and the config
// file format.
func TestRunHelp(t *testing.T) {
	out, errOut, code := runCLI(t, []string{"--help"}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d", code, exitOK)
	}
	if errOut != "" {
		t.Errorf("--help wrote to stderr: %q", errOut)
	}
	for _, want := range []string{"-mode", "-max-width", "-check", "-diff", "-stdout", "-force", "-config", "-no-gitignore", "-hard-breaks", "-version", "Exit codes", "mdreflow.yaml", "Examples"} {
		if !strings.Contains(out, want) {
			t.Errorf("--help output missing %q", want)
		}
	}
	// The help text is a review gate (docs/design.md): it must not carry
	// a stale claim that something is unimplemented.
	for _, unwanted := range []string{"not implemented", "M5", "reserved"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("--help output still contains stale text %q", unwanted)
		}
	}
}

// --- --version ---

// --version, like --help, is informational: one line on stdout, exit 0,
// nothing read from stdin and nothing written to disk.
func TestRunVersion(t *testing.T) {
	out, errOut, code := runCLI(t, []string{"--version"}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	if n := strings.Count(out, "\n"); n != 1 {
		t.Errorf("--version output has %d newlines, want exactly 1: %q", n, out)
	}
	for _, want := range []string{"mdreflow ", "commit ", "built ", "go1.", "goldmark "} {
		if !strings.Contains(out, want) {
			t.Errorf("--version output %q missing %q", out, want)
		}
	}
}

// --- helpers for git-backed tests ---

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func mustGitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}
