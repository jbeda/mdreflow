package main

import (
	"path/filepath"

	"github.com/jbeda/mdreflow/internal/exclude"
)

// excluder combines the (run-wide) gitignore repository root with
// per-directory config exclude: patterns to answer whether a path is
// excluded (docs/design.md's "Excludes" section). It memoizes one
// exclude.Matcher per distinct config directory encountered, since the
// same nearest .mdreflow.yaml is typically shared by many files during a
// directory walk.
type excluder struct {
	noGitignore bool
	repoRoot    string
	explicitCfg string
	cfgCache    *configCache
	matchers    map[string]*exclude.Matcher // key: config directory ("" = no config found)
}

func newExcluder(noGitignore bool, repoRoot, explicitCfg string, cfgCache *configCache) *excluder {
	return &excluder{
		noGitignore: noGitignore,
		repoRoot:    repoRoot,
		explicitCfg: explicitCfg,
		cfgCache:    cfgCache,
		matchers:    map[string]*exclude.Matcher{},
	}
}

// check reports whether path is excluded and by which source.
// walkRoot is the directory this invocation is processing under — the
// walked directory argument, or an explicit file's parent — and bounds
// the built-in-name exclusion when no repo/config scope exists.
func (e *excluder) check(path string, isDir bool, walkRoot string) (bool, exclude.Source, error) {
	dir := path
	if !isDir {
		dir = filepath.Dir(path)
	}
	cfg, cfgDir, err := e.cfgCache.resolve(dir, e.explicitCfg)
	if err != nil {
		return false, "", err
	}

	var patterns []string
	if cfg != nil {
		patterns = cfg.Exclude
	}
	m, ok := e.matchers[cfgDir]
	if !ok {
		nm, err := exclude.NewMatcher(e.repoRoot, e.noGitignore, patterns, cfgDir)
		if err != nil {
			return false, "", err
		}
		e.matchers[cfgDir] = nm
		m = nm
	}
	return m.Excluded(path, isDir, walkRoot)
}
