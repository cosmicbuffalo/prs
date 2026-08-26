package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/lipgloss"
)

// Config is the user-editable configuration loaded from config.json at startup.
// Every field is optional; anything unset keeps its built-in default.
type Config struct {
	Review ReviewConfig `json:"review"`
}

// ReviewConfig overrides the colors and glyphs used to render review states in
// the detail pane's Review Status section and the compact per-PR review-icon
// sequence. Colors are any lipgloss color string — an ANSI 256 index ("2",
// "15") or a hex value ("#5fafff"). Glyphs are arbitrary strings, typically a
// single glyph (e.g. "⭑", "✓", "✗"). Empty fields fall back to the defaults in
// styles.go.
//
//	"trusted" = a codeowner / trusted-reviewer-satisfying approval
//	"regular" = a valid approval, but not from the trusted-reviewer team
//	"changes" = a change request
type ReviewConfig struct {
	TrustedColor string `json:"trusted_color"`
	RegularColor string `json:"regular_color"`
	ChangesColor string `json:"changes_color"`
	TrustedGlyph string `json:"trusted_glyph"`
	RegularGlyph string `json:"regular_glyph"`
	ChangesGlyph string `json:"changes_glyph"`
}

// configBaseDir returns the directory config.json lives in: $PRS_CONFIG_DIR if
// set, else $XDG_CONFIG_HOME/prs, else ~/.config/prs. Config is kept separate
// from the state/cache directory (see stateBaseDir) because it's meant to be
// hand-edited.
func configBaseDir() (string, error) {
	if dir := os.Getenv("PRS_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "prs"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "prs"), nil
}

// configPath returns the full path to config.json (without creating anything —
// the file is optional and read-only from prs's perspective).
func configPath() (string, error) {
	dir, err := configBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// LoadConfig reads and parses config.json. A missing file yields the zero
// Config (all defaults) with no error. A present-but-unreadable or malformed
// file returns an error so the caller can surface it — but a bad config should
// never block the TUI from opening, so callers apply the returned (zero) Config
// regardless and just warn.
func LoadConfig() (Config, error) {
	var cfg Config
	path, err := configPath()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// apply overrides the package-level review styling defaults (see styles.go) with
// any values set in the config, leaving empty fields at their defaults. Called
// once at startup before the program runs, since the defaults are read at
// render time.
func (c Config) apply() {
	if v := c.Review.TrustedColor; v != "" {
		reviewTrustedColor = lipgloss.Color(v)
	}
	if v := c.Review.RegularColor; v != "" {
		reviewRegularColor = lipgloss.Color(v)
	}
	if v := c.Review.ChangesColor; v != "" {
		reviewChangesColor = lipgloss.Color(v)
	}
	if v := c.Review.TrustedGlyph; v != "" {
		reviewTrustedGlyph = v
	}
	if v := c.Review.RegularGlyph; v != "" {
		reviewRegularGlyph = v
	}
	if v := c.Review.ChangesGlyph; v != "" {
		reviewChangesGlyph = v
	}
}
