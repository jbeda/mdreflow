// Package config loads and discovers .mdreflow.yaml configuration files
// (docs/design.md's "Configuration" section) and merges them with CLI
// flag values under the documented precedence: flags > config file >
// built-in defaults.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

// FileName is the configuration file name discovered upward from each
// target (docs/design.md's Configuration section).
const FileName = ".mdreflow.yaml"

// MaxFileSize is the largest .mdreflow.yaml Load will read. A real
// config is well under 1 KB; capping at 1 MiB refuses a hostile or
// accidental multi-gigabyte file before it is ever read into memory
// (security review S2).
const MaxFileSize = 1 << 20 // 1 MiB

// maxNestingDepth and maxAliasRefs bound a cheap pre-parse scan (see
// scanForBombs) that rejects two classes of hostile .mdreflow.yaml
// before goccy/go-yaml ever sees them (security review S2): pathological
// bracket/brace nesting, which is a parse-time cost that never reaches
// Strict()'s unknown-key check, and YAML alias chains, whose expansion
// cost is exponential in the number of nested anchors (ten anchors of
// nine elements each expands to roughly 3.5e9 scalars and OOMs the
// process). A legitimate .mdreflow.yaml never nests a handful of list
// levels deep and never uses anchors or aliases at all — Mode,
// MaxWidth, Abbreviations, and Exclude are all plain scalars or flat
// lists — so both limits are far below anything a real config needs.
// maxAliasRefs is set well below the "100" a literal
// alias-reference count might suggest: the review's own 10-anchor,
// 9-way-fan-out proof of concept produces only 82 literal '*name'
// tokens (the exponential blowup comes from repeated expansion, not
// from the source text being large), so a threshold of 100 would not
// catch it.
const (
	maxNestingDepth = 64
	maxAliasRefs    = 50
)

// File is the parsed shape of .mdreflow.yaml. All fields are optional;
// the zero value of every field means "not set in this file" and defers
// to the built-in default, except Exclude/Abbreviations which are
// treated as additive lists.
type File struct {
	Mode          string   `yaml:"mode"`
	Dialect       string   `yaml:"dialect"`
	MaxWidth      int      `yaml:"max-width"`
	Abbreviations []string `yaml:"abbreviations"`
	Exclude       []string `yaml:"exclude"`
}

// Load parses path as a .mdreflow.yaml file. Unknown keys are a loud
// error (exit 2 at the CLI layer) — agents typo config keys, and a
// silently-ignored key is a worse failure mode than a hard stop.
//
// Load refuses (also exit 2) a file larger than MaxFileSize, and a file
// that passes size-wise but is engineered to blow up the YAML parser or
// decoder — see scanForBombs. goccy/go-yaml v1.19.2 (the version this
// module currently pins, and, checked against the module proxy while
// hardening this, still the newest release) has no alias-expansion or
// nesting-depth DecodeOption to lean on instead; its only built-in guard
// is an internal, unconfigurable 10000-level decode-depth cap that is
// far too high to help here and, per its own comments, exists to catch
// self-recursive aliases rather than to bound cost.
func Load(path string) (*File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxFileSize {
		return nil, fmt.Errorf("%s: config file too large (%d bytes, max %d)", path, info.Size(), MaxFileSize)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := scanForBombs(data); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	var f File
	if err := yaml.UnmarshalWithOptions(data, &f, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// scanForBombs is a cheap, non-semantic pre-scan over raw config bytes
// that rejects the two hostile-YAML shapes documented at maxNestingDepth
// and maxAliasRefs. It deliberately does not track quoting or comments:
// it only needs to never miss a real bracket/brace or alias, not to
// avoid ever over-counting one inside a string, so a loose byte scan is
// enough and cannot be tricked into passing through a genuinely
// pathological file.
func scanForBombs(data []byte) error {
	depth, maxDepth := 0, 0
	aliases := 0
	for i, b := range data {
		switch b {
		case '[', '{':
			depth++
			if depth > maxDepth {
				maxDepth = depth
			}
		case ']', '}':
			if depth > 0 {
				depth--
			}
		case '*':
			// A YAML alias node is '*' immediately followed by an
			// anchor-name character, at the start of a value position
			// (start of line, or just after whitespace/a flow
			// delimiter). That boundary requirement is what keeps this
			// from matching glob patterns in exclude: entries — e.g.
			// "generated/**" or "**/*.md" — since their '*'s are always
			// preceded by another non-boundary character.
			if i+1 < len(data) && isAliasNameByte(data[i+1]) && (i == 0 || isAliasBoundaryByte(data[i-1])) {
				aliases++
			}
		}
	}
	if maxDepth > maxNestingDepth {
		return fmt.Errorf("config nesting depth %d exceeds limit of %d", maxDepth, maxNestingDepth)
	}
	if aliases > maxAliasRefs {
		return fmt.Errorf("config contains %d YAML alias references, exceeds limit of %d", aliases, maxAliasRefs)
	}
	return nil
}

func isAliasNameByte(b byte) bool {
	return b == '_' || b == '-' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isAliasBoundaryByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '[', '{', ',', ':':
		return true
	default:
		return false
	}
}

// Discover walks upward from dir looking for FileName, returning the
// first match's path. If none is found, it returns ("", nil) — no
// config file is not an error; the built-in defaults apply.
//
// The walk stops at boundary, inclusive: a config file directly inside
// boundary still applies, but boundary's ancestors are never consulted.
// Callers should pass the enclosing git repository root when dir is
// inside one, falling back to the invoking user's home directory
// otherwise (cmd/mdreflow computes this once per run and threads it
// through configCache). Without a boundary, discovery would otherwise
// walk all the way to the filesystem root, so an attacker with write
// access to any ancestor directory — a world-writable /tmp, a shared CI
// workspace — could plant a .mdreflow.yaml that silently governs
// everything formatted beneath it (security review S5). Pass "" to
// fall back to that pre-hardening unbounded behavior explicitly (e.g. a
// test that wants to observe discovery in isolation); production
// callers should always have a real boundary available.
func Discover(dir, boundary string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	var absBoundary string
	if boundary != "" {
		absBoundary, err = filepath.Abs(boundary)
		if err != nil {
			return "", err
		}
	}
	for {
		candidate := filepath.Join(dir, FileName)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if absBoundary != "" && dir == absBoundary {
			return "", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
