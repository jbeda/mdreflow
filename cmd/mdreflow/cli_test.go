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

// TestRunInPlaceWritePreservesModeAndReplacesInode pins security review
// S6's atomic-write fix: the file's permission mode survives the
// temp-file-then-rename dance, and no ".mdreflow-*" temp file is left
// behind in the directory afterward.
func TestRunInPlaceWritePreservesModeAndReplacesInode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, unformatted)
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}

	_, errOut, code := runCLI(t, []string{p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Errorf("mode after in-place write = %v, want 0640", info.Mode().Perm())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".mdreflow-") {
			t.Errorf("leftover temp file %q after a successful write", e.Name())
		}
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

// TestRunConfigDiscoveryStopsAtRepoRoot pins security review S5: a
// .mdreflow.yaml planted outside the repository (here, in its
// grandparent) must not govern files inside it. The ancestor config
// excludes everything; if discovery incorrectly walked past the repo
// root and found it, a.md would be silently skipped and left
// unformatted. With the boundary in place, discovery stops at the repo
// root, the ancestor config is never consulted, and a.md is reformatted
// normally.
func TestRunConfigDiscoveryStopsAtRepoRoot(t *testing.T) {
	requireGit(t)
	base := t.TempDir()
	writeFile(t, filepath.Join(base, ".mdreflow.yaml"), "exclude:\n  - \"**/*.md\"\n")

	repo := filepath.Join(base, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGitInit(t, repo)
	p := filepath.Join(repo, "a.md")
	writeFile(t, p, unformatted)

	_, errOut, code := runCLI(t, []string{repo}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	if got := readFile(t, p); got != formatted {
		t.Errorf("a.md should have been reformatted (ancestor config outside the repo must not apply), got %q", got)
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

// --- symlinks and non-regular files (security review S3/S4) ---

func TestRunExplicitSymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.md")
	writeFile(t, outside, "line one\nline two\n")
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	_, errOut, code := runCLI(t, []string{link}, "")
	if code != exitRefused {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitRefused, errOut)
	}
	if !strings.Contains(errOut, "not a regular file") || !strings.Contains(errOut, "symlink") {
		t.Errorf("stderr = %q, want a not-a-regular-file/symlink message", errOut)
	}
	if got := readFile(t, outside); got != "line one\nline two\n" {
		t.Errorf("symlink target must not be written through, got %q", got)
	}
}

// TestRunExplicitSymlinkForced: --force skips the regular-file refusal,
// so the read still follows the symlink and formats the target's
// content — but the atomic write (security review S6) renames the
// result into place at the named path, which replaces the symlink
// itself rather than following it through to the target (S3's write
// half is closed "for free" by the same rename, per the review). So
// --force on a symlink formats what the symlink pointed to, but leaves
// that original target file untouched and turns the symlink into an
// ordinary file holding the formatted content.
func TestRunExplicitSymlinkForced(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.md")
	writeFile(t, outside, unformatted)
	link := filepath.Join(dir, "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	_, errOut, code := runCLI(t, []string{"--force", link}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	if got := readFile(t, outside); got != unformatted {
		t.Errorf("the original symlink target must be left untouched, got %q", got)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("the atomic rename should have replaced the symlink with a regular file")
	}
	if got := readFile(t, link); got != formatted {
		t.Errorf("link.md content = %q, want the formatted content", got)
	}
}

func TestRunDirectoryWalkSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.md")
	writeFile(t, outside, unformatted)
	writeFile(t, filepath.Join(dir, "repo", "keep.md"), unformatted)
	link := filepath.Join(dir, "repo", "link.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	_, _, code := runCLI(t, []string{filepath.Join(dir, "repo")}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d", code, exitOK)
	}
	if got := readFile(t, filepath.Join(dir, "repo", "keep.md")); got != formatted {
		t.Errorf("keep.md should have been reformatted, got %q", got)
	}
	if got := readFile(t, outside); got != unformatted {
		t.Errorf("a symlink picked up by a directory walk must be skipped silently, target = %q", got)
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

// --- hard-break spelling ---

// TestRunHardBreakSpellingPromotion checks the CLI end to end: a
// trailing-double-space hard break is promoted to a backslash, since the
// hard-break spelling is now decided by the source, not a flag (there is
// no --hard-breaks flag).
func TestRunHardBreakSpellingPromotion(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, "Line one.  \nLine two.\n")

	_, errOut, code := runCLI(t, []string{p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	got := readFile(t, p)
	if !strings.Contains(got, "\\\n") {
		t.Errorf("expected a backslash hard break in output, got %q", got)
	}
}

// TestRunHardBreaksFlagRemoved checks that --hard-breaks is rejected: the
// flag was removed when the hard-break policy stopped being configurable.
func TestRunHardBreaksFlagRemoved(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, "Line one.\n")

	_, errOut, code := runCLI(t, []string{"--hard-breaks=backslash", p}, "")
	if code != exitUsage {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitUsage, errOut)
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
	for _, want := range []string{"-mode", "-max-width", "-check", "-diff", "-stdout", "-force", "-config", "-no-gitignore", "-version", "Exit codes", "mdreflow.yaml", "Examples"} {
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

// --- --dialect ---

const admonitionDoc = "!!! tip \"T\"\n\n    Use the selector to switch. A capability listed\n    under dev has not shipped.\n"

func TestRunDialectMkDocsReflowsAdmonitionBody(t *testing.T) {
	out, errOut, code := runCLI(t, []string{"--dialect=mkdocs"}, admonitionDoc)
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	want := "!!! tip \"T\"\n\n    Use the selector to switch.\n    A capability listed under dev has not shipped.\n"
	if out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
}

func TestRunDialectDefaultLeavesAdmonitionBodyAlone(t *testing.T) {
	out, errOut, code := runCLI(t, nil, admonitionDoc)
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	if out != admonitionDoc {
		t.Errorf("stdout = %q, want input unchanged", out)
	}
}

func TestRunDialectConfigKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mdreflow.yaml"), "dialect: mkdocs\n")
	p := filepath.Join(dir, "a.md")
	writeFile(t, p, admonitionDoc)

	_, errOut, code := runCLI(t, []string{p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	if got := readFile(t, p); got == admonitionDoc {
		t.Errorf("config dialect: mkdocs did not reflow the admonition body")
	}
}

// "commonmark" is reserved for a future strict profile; accepting it as
// an alias for the gfm default would burn the name on the one profile it
// doesn't describe.
func TestRunDialectCommonmarkIsReservedError(t *testing.T) {
	_, errOut, code := runCLI(t, []string{"--dialect=commonmark"}, "Hi.\n")
	if code != exitUsage {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "future") || !strings.Contains(errOut, "gfm") {
		t.Errorf("stderr = %q, want a kept-for-the-future explanation pointing at gfm", errOut)
	}
}

func TestRunDialectUnknownIsUsageError(t *testing.T) {
	_, errOut, code := runCLI(t, []string{"--dialect=mkdoc"}, "Hi.\n")
	if code != exitUsage {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut, "mkdoc") {
		t.Errorf("stderr = %q, want it to name the bad value", errOut)
	}
}

// --- --explain ---

func TestRunExplainReportsFrozenParagraphsToStderr(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	// One reflowable paragraph plus one frozen by the def zone: the
	// reflow still happens, the frozen paragraph is reported on stderr
	// with its location, stable reason code, and a remediation line, and
	// stdout stays empty (in-place mode).
	writeFile(t, p, unformatted+"\n[label]: /url\nfrozen neighbor prose\n")

	out, errOut, code := runCLI(t, []string{"--explain", p}, "")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty (explain output belongs on stderr)", out)
	}
	if !strings.Contains(errOut, p+":4: skipped:") {
		t.Errorf("stderr = %q, want a %q record", errOut, p+":4: skipped:")
	}
	if !strings.Contains(errOut, "[link-ref-def-neighbor]") {
		t.Errorf("stderr = %q, want the stable reason code in brackets", errOut)
	}
	if !strings.Contains(errOut, "\n  ") {
		t.Errorf("stderr = %q, want an indented remediation line", errOut)
	}
	if !strings.Contains(readFile(t, p), formatted) {
		t.Errorf("file was not reformatted alongside --explain")
	}
}

func TestRunExplainCleanFileReportsNothing(t *testing.T) {
	_, errOut, code := runCLI(t, []string{"--explain"}, "Hi there.\n")
	if code != exitOK {
		t.Fatalf("exit=%d, want %d; stderr=%q", code, exitOK, errOut)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want empty for a document with nothing frozen", errOut)
	}
}
