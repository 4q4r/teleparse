package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/4q4r/teleparse/internal/filters"

	toml "github.com/pelletier/go-toml/v2"
)

// Sentinel errors for TOML handling.
var (
	ErrEncodeConfig  = errors.New("encode config")
	ErrWriteConfig   = errors.New("write config")
	ErrMakeConfigDir = errors.New("create config dir")
	ErrDecodeTOML    = errors.New("decode toml")
)

// Filters is the filter-options surface shared by config, profiles and flags.
type Filters = filters.Options

// Recursion mirrors filters.Recursion for config consumers.
type Recursion = filters.Recursion

// decodeTOML merges TOML text into cfg. Unknown keys are rejected (typos must fail loudly).
func decodeTOML(text string, cfg *Config) error {
	dec := toml.NewDecoder(bytes.NewReader([]byte(text)))

	dec.DisallowUnknownFields()

	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("%w: %w", ErrDecodeTOML, err)
	}

	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Filters{}
	}

	if err := applySidecarCompat(text, cfg); err != nil {
		return err
	}

	normalizeOutputEnums(cfg)

	return nil
}

// applySidecarCompat migrates the deprecated output.sidecar bool onto
// output.metadata: a config setting sidecar without metadata maps true to
// "file" and false to "off"; an explicit metadata key always wins and
// configs naming neither key keep the built-in default.
func applySidecarCompat(text string, cfg *Config) error {
	var raw map[string]any

	if err := toml.Unmarshal([]byte(text), &raw); err != nil {
		return fmt.Errorf("%w: scan output keys: %w", ErrDecodeTOML, err)
	}

	output, ok := raw["output"].(map[string]any)
	if !ok {
		return nil
	}

	if _, hasMetadata := output["metadata"]; hasMetadata {
		return nil
	}

	sidecar, hasSidecar := output["sidecar"].(bool)
	if !hasSidecar {
		return nil
	}

	if sidecar {
		cfg.Output.Metadata = MetadataFile

		return nil
	}

	cfg.Output.Metadata = MetadataOff

	return nil
}

// normalizeOutputEnums folds empty naming/metadata values onto their
// built-in defaults so downstream consumers compare against exactly one
// non-empty mode, never a fallback chain.
func normalizeOutputEnums(cfg *Config) {
	if cfg.Output.Naming == "" {
		cfg.Output.Naming = NamingOriginal
	}

	if cfg.Output.Metadata == "" {
		cfg.Output.Metadata = MetadataChat
	}
}

// save writes cfg as TOML to path with 0600, creating parent dirs.
func (c *Config) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return fmt.Errorf("%w: %w", ErrMakeConfigDir, err)
	}

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)

	enc.SetIndentTables(true)

	if err := enc.Encode(c); err != nil {
		return fmt.Errorf("%w: %w", ErrEncodeConfig, err)
	}

	if err := os.WriteFile(path, buf.Bytes(), filePerm); err != nil {
		return fmt.Errorf("%w: %w", ErrWriteConfig, err)
	}

	return nil
}

// fillProfile overlays non-zero fields of overlay onto base into dst
// (profile-over-defaults semantics).
func fillProfile(dst, base, overlay *Filters) {
	*dst = *base

	if overlay == nil {
		return
	}

	if len(overlay.Media) > 0 {
		dst.Media = overlay.Media
	}

	if overlay.Dedupe != "" {
		dst.Dedupe = overlay.Dedupe
	}

	if overlay.Recursion != (Recursion{}) {
		dst.Recursion = overlay.Recursion
	}
}

// Save writes the config as TOML to path with 0600, creating parent dirs.
func (c *Config) Save(path string) error {
	return c.save(path)
}
