// Package exclude decides whether a path should be skipped by mdreflow's
// CLI: repo .gitignore patterns, config-file exclude patterns, and a
// small set of always-excluded directories (docs/design.md's "Excludes"
// section).
//
// Matching is delegated to github.com/boyter/gocodewalker's vendored
// gitignore engine (github.com/boyter/gocodewalker/go-gitignore), which
// implements full nested-.gitignore repository semantics — closer
// .gitignore files override farther ones, negation, directory-only
// patterns, anchoring, and "a child cannot be un-ignored if its parent
// directory is ignored". That is more than a single-file pattern matcher
// (e.g. sabhiram/go-gitignore) provides, and is required for repository
// discovery of nested .gitignore files. See docs/design.md's parity-test
// requirement: this package's behavior is checked against `git
// check-ignore` on a synthetic tree in exclude_test.go.
package exclude

import (
	"os"
	"path/filepath"
	"strings"

	gitignore "github.com/boyter/gocodewalker/go-gitignore"
)

// Source identifies which layer excluded a path, for the CLI's
// "skipped (excluded by <source>)" message.
type Source string

const (
	SourceGitignore Source = "gitignore"
	SourceConfig    Source = "config"
	SourceBuiltin   Source = "built-in"
)

// builtinNames are always excluded, regardless of config or gitignore —
// docs/design.md's "Built-in always-excludes".
var builtinNames = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
}

// Matcher answers whether a path is excluded and by which source.
type Matcher struct {
	repo      gitignore.GitIgnore // nested-.gitignore repository matcher; nil if disabled or unavailable
	config    gitignore.GitIgnore // config `exclude:` patterns; nil if none configured
	scopeRoot string              // bounds the built-in-name check (see Excluded); "" = unbounded
}

// NewMatcher builds a Matcher.
//
// gitignoreRoot is the directory to treat as the repository root for
// .gitignore discovery (typically the nearest ancestor containing
// .git); if empty or noGitignore is true, .gitignore files are not
// consulted.
//
// configPatterns are gitignore-syntax patterns from a .mdreflow.yaml's
// exclude: list, rooted at configBase (the directory containing that
// config file). configPatterns may be empty.
func NewMatcher(gitignoreRoot string, noGitignore bool, configPatterns []string, configBase string) (*Matcher, error) {
	m := &Matcher{}
	switch {
	case gitignoreRoot != "":
		m.scopeRoot = gitignoreRoot
	case configBase != "":
		m.scopeRoot = configBase
	}

	if !noGitignore && gitignoreRoot != "" {
		repo, err := gitignore.NewRepository(gitignoreRoot)
		if err != nil {
			return nil, err
		}
		m.repo = repo
	}

	if len(configPatterns) > 0 {
		base := configBase
		if base == "" {
			base = "."
		}
		absBase, err := filepath.Abs(base)
		if err != nil {
			return nil, err
		}
		m.config = gitignore.New(strings.NewReader(strings.Join(configPatterns, "\n")), absBase, nil)
	}

	return m, nil
}

// Excluded reports whether path (a file or directory) is excluded, and
// by which source. path may be relative or absolute; it is resolved to
// an absolute path for matching. isDir must accurately reflect whether
// path is a directory (gitignore directory-only patterns, e.g.
// "build/", depend on it).
func (m *Matcher) Excluded(path string, isDir bool) (bool, Source, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, "", err
	}

	// A path can never exclude itself as its own gitignore/config base
	// (and the vendored gitignore matcher panics on an exact
	// path-equals-base match — repository.Absolute assumes there is
	// always a path separator after the base to strip).
	if (m.repo != nil && abs == m.repo.Base()) || (m.config != nil && abs == m.config.Base()) {
		return false, "", nil
	}

	// The built-in-name check is scoped to scopeRoot (typically the
	// repository root, or the config file's directory as a fallback) so
	// an unrelated ancestor directory that happens to be named "vendor"
	// or similar doesn't spuriously exclude everything below it.
	builtinScope := abs
	if m.scopeRoot != "" {
		if rel, err := filepath.Rel(m.scopeRoot, abs); err == nil && !strings.HasPrefix(rel, "..") {
			builtinScope = rel
		}
	}
	for _, part := range strings.Split(filepath.ToSlash(builtinScope), "/") {
		if builtinNames[part] {
			return true, SourceBuiltin, nil
		}
	}

	if m.repo != nil {
		if match := m.repo.Absolute(abs, isDir); match != nil && match.Ignore() {
			return true, SourceGitignore, nil
		}
	}

	if m.config != nil {
		if match := m.config.Absolute(abs, isDir); match != nil && match.Ignore() {
			return true, SourceConfig, nil
		}
	}

	return false, "", nil
}

// FindRepoRoot walks upward from start looking for a .git entry (file or
// directory — a plain file marks a submodule/worktree). It returns the
// directory containing .git, or "" if none is found before the
// filesystem root.
func FindRepoRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		dir = filepath.Dir(dir)
	}
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
