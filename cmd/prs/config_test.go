package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// saveReviewStyle snapshots the package-level review styling globals and
// restores them when the test ends, so a test that calls Config.apply doesn't
// leak overrides into other tests.
func saveReviewStyle(t *testing.T) {
	t.Helper()
	tc, rc, cc := reviewTrustedColor, reviewRegularColor, reviewChangesColor
	tg, rg, cg := reviewTrustedGlyph, reviewRegularGlyph, reviewChangesGlyph
	t.Cleanup(func() {
		reviewTrustedColor, reviewRegularColor, reviewChangesColor = tc, rc, cc
		reviewTrustedGlyph, reviewRegularGlyph, reviewChangesGlyph = tg, rg, cg
	})
}

func TestConfigApplyOverridesSetFieldsOnly(t *testing.T) {
	saveReviewStyle(t)
	defaultRegularColor := reviewRegularColor
	defaultChangesGlyph := reviewChangesGlyph

	cfg := Config{Review: ReviewConfig{
		TrustedColor: "#ff8800",
		RegularGlyph: "√",
	}}
	cfg.apply()

	if reviewTrustedColor != lipgloss.Color("#ff8800") {
		t.Errorf("trusted color = %v, want #ff8800", reviewTrustedColor)
	}
	if reviewRegularGlyph != "√" {
		t.Errorf("regular glyph = %q, want √", reviewRegularGlyph)
	}
	// Fields left empty in the config must keep their defaults.
	if reviewRegularColor != defaultRegularColor {
		t.Errorf("regular color = %v, want unchanged %v", reviewRegularColor, defaultRegularColor)
	}
	if reviewChangesGlyph != defaultChangesGlyph {
		t.Errorf("changes glyph = %q, want unchanged %q", reviewChangesGlyph, defaultChangesGlyph)
	}
}

func TestLoadConfigMissingFileIsDefaults(t *testing.T) {
	t.Setenv("PRS_CONFIG_DIR", t.TempDir()) // empty dir → no config.json
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig on a missing file should not error: %v", err)
	}
	if cfg != (Config{}) {
		t.Errorf("missing config should yield the zero Config, got %+v", cfg)
	}
}

func TestLoadConfigParsesFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PRS_CONFIG_DIR", dir)
	data := `{"review":{"trusted_glyph":"★","changes_color":"9"}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Review.TrustedGlyph != "★" {
		t.Errorf("trusted glyph = %q, want ★", cfg.Review.TrustedGlyph)
	}
	if cfg.Review.ChangesColor != "9" {
		t.Errorf("changes color = %q, want 9", cfg.Review.ChangesColor)
	}
}

func TestLoadConfigMalformedErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PRS_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(); err == nil {
		t.Error("LoadConfig should return an error for malformed JSON")
	}
}

// TestConfigUpdatesCheckToggle verifies the updates.check field parses and that
// apply() toggles the updateCheckEnabled global only when the field is set.
func TestConfigUpdatesCheckToggle(t *testing.T) {
	saved := updateCheckEnabled
	t.Cleanup(func() { updateCheckEnabled = saved })

	// Explicit false turns it off.
	updateCheckEnabled = true
	Config{Updates: UpdatesConfig{Check: boolPtr(false)}}.apply()
	if updateCheckEnabled {
		t.Error("updates.check=false should disable the update check")
	}

	// Explicit true turns it on.
	updateCheckEnabled = false
	Config{Updates: UpdatesConfig{Check: boolPtr(true)}}.apply()
	if !updateCheckEnabled {
		t.Error("updates.check=true should enable the update check")
	}

	// Omitted (nil) leaves the current value untouched.
	updateCheckEnabled = true
	Config{}.apply()
	if !updateCheckEnabled {
		t.Error("omitting updates.check should leave the default (on) unchanged")
	}
}

func TestLoadConfigParsesUpdatesCheck(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PRS_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"updates":{"check":false}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Updates.Check == nil || *cfg.Updates.Check != false {
		t.Errorf("updates.check = %v, want a pointer to false", cfg.Updates.Check)
	}
}

func boolPtr(b bool) *bool { return &b }

// TestReviewMark verifies the glyph/color selection precedence: a change request
// always wins over IsCodeowner; a trusted approval gets the trusted mark; any
// other approval gets the regular mark.
func TestReviewMark(t *testing.T) {
	saveReviewStyle(t)
	reviewTrustedGlyph, reviewRegularGlyph, reviewChangesGlyph = "T", "R", "C"
	reviewTrustedColor, reviewRegularColor, reviewChangesColor = lipgloss.Color("1"), lipgloss.Color("2"), lipgloss.Color("3")

	cases := []struct {
		name      string
		ev        ReviewEvent
		wantGlyph string
		wantColor lipgloss.Color
	}{
		{"trusted approval", ReviewEvent{State: ReviewApproved, IsCodeowner: true}, "T", "1"},
		{"regular approval", ReviewEvent{State: ReviewApproved, IsCodeowner: false}, "R", "2"},
		{"changes requested", ReviewEvent{State: ReviewChangesRequested, IsCodeowner: false}, "C", "3"},
		{"changes requested by codeowner still shows changes", ReviewEvent{State: ReviewChangesRequested, IsCodeowner: true}, "C", "3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, col := reviewMark(c.ev)
			if g != c.wantGlyph || col != c.wantColor {
				t.Errorf("reviewMark = (%q, %v), want (%q, %v)", g, col, c.wantGlyph, c.wantColor)
			}
		})
	}
}
