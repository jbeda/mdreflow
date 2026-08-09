package main

import (
	"fmt"
	"path/filepath"

	"github.com/jbeda/mdreflow"
	"github.com/jbeda/mdreflow/internal/config"
)

// flags holds the parsed CLI flag values plus which of them were
// explicitly set on the command line (docs/design.md's precedence rule:
// flags > config file > built-in defaults, and an explicitly-set flag
// beats config even at its zero value).
type flags struct {
	mode                        string
	maxWidth                    int
	check                       bool
	diff                        bool
	stdout                      bool
	force                       bool
	configPath                  string
	noGitignore                 bool
	hardBreaks                  string
	stripSentenceTerminalBreaks bool
	version                     bool

	set map[string]bool // flag name -> explicitly set on the command line
}

func (f *flags) isSet(name string) bool { return f.set[name] }

func parseMode(s string) (mdreflow.Mode, error) {
	switch s {
	case "sentence":
		return mdreflow.ModeSentence, nil
	case "para":
		return mdreflow.ModePara, nil
	case "wrap":
		return mdreflow.ModeWrap, nil
	default:
		return 0, fmt.Errorf("unsupported mode %q (want one of: sentence, para, wrap)", s)
	}
}

func parseHardBreaks(s string) (mdreflow.HardBreakStyle, error) {
	switch s {
	case "br":
		return mdreflow.HardBreakBr, nil
	case "spaces":
		return mdreflow.HardBreakSpaces, nil
	case "backslash":
		return mdreflow.HardBreakBackslash, nil
	default:
		return 0, fmt.Errorf("unsupported hard-breaks %q (want one of: br, spaces, backslash)", s)
	}
}

// configCache memoizes upward config discovery (by starting directory)
// and config file parsing (by config file path), since the same
// .mdreflow.yaml is typically discovered from many files during a
// directory walk.
type configCache struct {
	discovered map[string]string       // starting dir -> discovered config path ("" = none found)
	loaded     map[string]*config.File // config path -> parsed file
	errs       map[string]error        // config path -> load error, if any
}

func newConfigCache() *configCache {
	return &configCache{
		discovered: map[string]string{},
		loaded:     map[string]*config.File{},
		errs:       map[string]error{},
	}
}

// resolve returns the config file applicable to a file living in dir,
// along with the directory that config file lives in (the base for its
// exclude: patterns). If explicitPath is non-empty, discovery is
// skipped and that file is used directly for every call.
func (c *configCache) resolve(dir, explicitPath string) (*config.File, string, error) {
	path := explicitPath
	if path == "" {
		if cached, ok := c.discovered[dir]; ok {
			path = cached
		} else {
			found, err := config.Discover(dir)
			if err != nil {
				return nil, "", err
			}
			c.discovered[dir] = found
			path = found
		}
	}
	if path == "" {
		return nil, "", nil
	}
	if f, ok := c.loaded[path]; ok {
		return f, filepath.Dir(path), nil
	}
	if err, ok := c.errs[path]; ok {
		return nil, "", err
	}
	f, err := config.Load(path)
	if err != nil {
		c.errs[path] = err
		return nil, "", err
	}
	c.loaded[path] = f
	return f, filepath.Dir(path), nil
}

// resolvedOptions is the outcome of merging built-in defaults, a
// discovered config file, and explicit CLI flags for one target.
type resolvedOptions struct {
	opts            mdreflow.Options
	excludePatterns []string
	excludeBase     string // directory the exclude patterns are rooted at
}

// mergeOptions applies docs/design.md's precedence: flags > config file
// > built-in defaults (mdreflow.Options{} zero value). cfg may be nil
// (no config file found or applicable, e.g. stdin with none discovered).
func mergeOptions(f *flags, cfg *config.File, cfgDir string) (resolvedOptions, error) {
	var r resolvedOptions
	opts := mdreflow.Options{}

	if cfg != nil {
		if cfg.Mode != "" {
			m, err := parseMode(cfg.Mode)
			if err != nil {
				return r, fmt.Errorf("config: %w", err)
			}
			opts.Mode = m
		}
		opts.MaxWidth = cfg.MaxWidth
		if cfg.HardBreaks != "" {
			hb, err := parseHardBreaks(cfg.HardBreaks)
			if err != nil {
				return r, fmt.Errorf("config: %w", err)
			}
			opts.HardBreaks = hb
		}
		opts.Abbreviations = append(opts.Abbreviations, cfg.Abbreviations...)
		r.excludePatterns = cfg.Exclude
		r.excludeBase = cfgDir
	}

	if f.isSet("mode") {
		m, err := parseMode(f.mode)
		if err != nil {
			return r, fmt.Errorf("--mode: %w", err)
		}
		opts.Mode = m
	}
	if f.isSet("max-width") {
		opts.MaxWidth = f.maxWidth
	}
	if f.isSet("hard-breaks") {
		hb, err := parseHardBreaks(f.hardBreaks)
		if err != nil {
			return r, fmt.Errorf("--hard-breaks: %w", err)
		}
		opts.HardBreaks = hb
	}
	// stripSentenceTerminalBreaks has no config-file key, so its flag
	// value is simply the answer.
	opts.StripSentenceTerminalBreaks = f.stripSentenceTerminalBreaks

	if opts.Mode == mdreflow.ModePara && opts.MaxWidth != 0 {
		return r, fmt.Errorf("--max-width is not valid with --mode=para (para mode always joins to a single line)")
	}

	r.opts = opts
	return r, nil
}
