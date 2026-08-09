package exclude

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestExcludedBuiltin(t *testing.T) {
	dir := t.TempDir()
	m, err := NewMatcher("", true, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".git", "node_modules", "vendor"} {
		excluded, source, err := m.Excluded(filepath.Join(dir, name, "x"), false, dir)
		if err != nil {
			t.Fatal(err)
		}
		if !excluded || source != SourceBuiltin {
			t.Errorf("path under %s: excluded=%v source=%q, want true/%q", name, excluded, source, SourceBuiltin)
		}
	}
	excluded, _, err := m.Excluded(filepath.Join(dir, "src", "x.md"), false, dir)
	if err != nil {
		t.Fatal(err)
	}
	if excluded {
		t.Errorf("ordinary path should not be excluded")
	}
}

// TestExcludedBuiltinScopedToWalkRoot pins the Go-quality review's S1
// finding: the built-in-name check must be scoped to path components
// below the walk root (here simulated via configBase, which sets
// scopeRoot the same way a repository root would), not the full
// absolute path. A "vendor" component that only appears in the walk
// root's own ancestry must not exclude anything; a "vendor" component
// that appears below the walk root still must.
func TestExcludedBuiltinScopedToWalkRoot(t *testing.T) {
	base := t.TempDir()
	walkRoot := filepath.Join(base, "vendor", "proj") // "vendor" is an ancestor of walkRoot
	if err := os.MkdirAll(walkRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	m, err := NewMatcher("", true, nil, walkRoot)
	if err != nil {
		t.Fatal(err)
	}

	ordinary := filepath.Join(walkRoot, "a.md")
	excluded, source, err := m.Excluded(ordinary, false, walkRoot)
	if err != nil {
		t.Fatal(err)
	}
	if excluded {
		t.Errorf("Excluded(%q) = true (source %q), want false: \"vendor\" only appears above the walk root", ordinary, source)
	}

	nested := filepath.Join(walkRoot, "vendor", "b.md") // "vendor" below the walk root
	excluded, source, err = m.Excluded(nested, false, walkRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !excluded || source != SourceBuiltin {
		t.Errorf("Excluded(%q) = %v (source %q), want true/%q: \"vendor\" is below the walk root", nested, excluded, source, SourceBuiltin)
	}
}

// TestExcludedNoScopeRootUsesWalkRoot pins the Go-quality review's S1
// finding end-to-end: with no gitignore root and no config directory
// (scopeRoot == "" at the NewMatcher call), the built-in-name check
// scopes to the caller-supplied walk root — an explicit file's own
// directory, or the walked directory argument — rather than scanning
// the whole absolute path. No Chdir: the review's repro invoked
// mdreflow from an unrelated working directory, which is exactly the
// case a CWD fallback would miss.
func TestExcludedNoScopeRootUsesWalkRoot(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "vendor", "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}

	m, err := NewMatcher("", true, nil, "")
	if err != nil {
		t.Fatal(err)
	}

	ordinary := filepath.Join(proj, "a.md")
	excluded, source, err := m.Excluded(ordinary, false, proj)
	if err != nil {
		t.Fatal(err)
	}
	if excluded {
		t.Errorf("Excluded(%q) = true (source %q), want false: \"vendor\" only appears above the walk root", ordinary, source)
	}

	nested := filepath.Join(proj, "vendor", "b.md")
	excluded, source, err = m.Excluded(nested, false, proj)
	if err != nil {
		t.Fatal(err)
	}
	if !excluded || source != SourceBuiltin {
		t.Errorf("Excluded(%q) = %v (source %q), want true/%q: \"vendor\" is below the walk root", nested, excluded, source, SourceBuiltin)
	}
}

func TestExcludedConfigPattern(t *testing.T) {
	dir := t.TempDir()
	m, err := NewMatcher("", true, []string{"generated/**", "CHANGELOG.md"}, dir)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{filepath.Join(dir, "CHANGELOG.md"), false, true},
		{filepath.Join(dir, "generated", "x.md"), false, true},
		{filepath.Join(dir, "README.md"), false, false},
	}
	for _, c := range cases {
		excluded, source, err := m.Excluded(c.path, c.isDir, dir)
		if err != nil {
			t.Fatal(err)
		}
		if excluded != c.want {
			t.Errorf("Excluded(%q) = %v (source %q), want %v", c.path, excluded, source, c.want)
		}
	}
}

// TestGitignoreParity is docs/design.md's Testing item 5: compare this
// package's gitignore matching against `git check-ignore --verbose` on a
// synthetic tree exercising negation, directory-only patterns, anchored
// patterns, nested .gitignore files, and "**". No Go gitignore
// implementation is fully spec-compliant (go-git#108), so this is the
// real defense against divergence — skipped gracefully if git isn't
// installed.
func TestGitignoreParity(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed; skipping gitignore parity test")
	}

	root := t.TempDir()
	// Resolve symlinks (macOS puts TMPDIR under /var, a symlink to
	// /private/var) so our absolute-path matching and git's agree on
	// the same path spelling.
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		".gitignore": "*.log\n!important.log\n/anchored.txt\nbuild/\n**/generated/\n",

		"a.log":         "x",
		"important.log": "x",
		"anchored.txt":  "x",
		"keep.md":       "x",

		"other/anchored.txt": "x",
		"other/keep.md":      "x",

		"build/output.md": "x",

		"deep/nested/generated/x.md": "x",
		"deep/generated/y.md":        "x",
		"deep/keep.md":               "x",

		"sub/.gitignore":     "local.tmp\n!keep/local.tmp\n",
		"sub/local.tmp":      "x",
		"sub/keep/local.tmp": "x",
		"sub/file.md":        "x",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gitPath, args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q")

	m, err := NewMatcher(root, false, nil, "")
	if err != nil {
		t.Fatal(err)
	}

	// Enumerate every file and directory in the tree (except .git) as a
	// candidate, so the comparison covers directories (build/, sub/keep/,
	// deep/generated/) as well as files.
	var candidates []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		candidates = append(candidates, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(candidates)

	for _, rel := range candidates {
		abs := filepath.Join(root, rel)
		info, err := os.Stat(abs)
		if err != nil {
			t.Fatal(err)
		}

		ourExcluded, _, err := m.Excluded(abs, info.IsDir(), root)
		if err != nil {
			t.Fatalf("Excluded(%q): %v", rel, err)
		}

		cmd := exec.Command(gitPath, "check-ignore", "--verbose", "--no-index", "--", rel)
		cmd.Dir = root
		out, runErr := cmd.CombinedOutput()
		var gitExcluded bool
		switch {
		case runErr == nil:
			// Exit 0 means some pattern matched — but with --verbose
			// that includes negated ("!pattern") matches, which mean
			// the path is explicitly NOT ignored. Inspect the printed
			// "<source>:<linenum>:<pattern>\t<path>" line to tell them
			// apart (git preserves the "!" prefix verbatim).
			line := strings.TrimSpace(string(out))
			pre, _, _ := strings.Cut(line, "\t")
			parts := strings.SplitN(pre, ":", 3)
			pattern := ""
			if len(parts) == 3 {
				pattern = parts[2]
			}
			gitExcluded = !strings.HasPrefix(pattern, "!")
		case isExitCode(runErr, 1):
			gitExcluded = false
		default:
			t.Fatalf("git check-ignore %q: %v\n%s", rel, runErr, out)
		}

		if ourExcluded != gitExcluded {
			t.Errorf("%s: our matcher says excluded=%v, git check-ignore says excluded=%v (git output: %s)", rel, ourExcluded, gitExcluded, strings.TrimSpace(string(out)))
		}
	}
}

func isExitCode(err error, code int) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == code
}
