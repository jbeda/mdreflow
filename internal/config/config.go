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

// File is the parsed shape of .mdreflow.yaml. All fields are optional;
// the zero value of every field means "not set in this file" and defers
// to the built-in default, except Exclude/Abbreviations which are
// treated as additive lists.
type File struct {
	Mode          string   `yaml:"mode"`
	MaxWidth      int      `yaml:"max-width"`
	Typography    []string `yaml:"typography"`
	HardBreaks    string   `yaml:"hard-breaks"`
	Abbreviations []string `yaml:"abbreviations"`
	Exclude       []string `yaml:"exclude"`
}

// Load parses path as a .mdreflow.yaml file. Unknown keys are a loud
// error (exit 2 at the CLI layer) — agents typo config keys, and a
// silently-ignored key is a worse failure mode than a hard stop.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f File
	if err := yaml.UnmarshalWithOptions(data, &f, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// Discover walks upward from dir looking for FileName, returning the
// first match's path. If none is found, it returns ("", nil) — no
// config file is not an error; the built-in defaults apply.
func Discover(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
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
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
