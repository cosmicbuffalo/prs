package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAssetName(t *testing.T) {
	cases := []struct {
		os, arch string
		want     string
		wantErr  bool
	}{
		{"linux", "amd64", "prs_linux_amd64.tar.gz", false},
		{"linux", "arm64", "prs_linux_arm64.tar.gz", false},
		{"darwin", "amd64", "prs_darwin_amd64.tar.gz", false},
		{"darwin", "arm64", "prs_darwin_arm64.tar.gz", false},
		{"windows", "amd64", "", true},
		{"linux", "386", "", true},
	}
	for _, c := range cases {
		got, err := assetName(c.os, c.arch)
		if c.wantErr {
			if err == nil {
				t.Errorf("assetName(%q,%q) = %q, want error", c.os, c.arch, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("assetName(%q,%q) = (%q,%v), want (%q,nil)", c.os, c.arch, got, err, c.want)
		}
	}
}

func TestParseLatestTag(t *testing.T) {
	tag, err := parseLatestTag([]byte(`{"tag_name":"v0.1.4","name":"v0.1.4"}`))
	if err != nil || tag != "v0.1.4" {
		t.Errorf("parseLatestTag = (%q,%v), want (v0.1.4,nil)", tag, err)
	}
	if _, err := parseLatestTag([]byte(`{"name":"x"}`)); err == nil {
		t.Error("parseLatestTag with no tag_name should error")
	}
	if _, err := parseLatestTag([]byte(`not json`)); err == nil {
		t.Error("parseLatestTag with bad JSON should error")
	}
}

func TestParseChecksum(t *testing.T) {
	sums := "abc123  prs_linux_amd64.tar.gz\n" +
		"def456  prs_darwin_arm64.tar.gz\n" +
		"999aaa *prs_linux_arm64.tar.gz\n" // binary-mode marker
	cases := []struct {
		asset string
		want  string
	}{
		{"prs_linux_amd64.tar.gz", "abc123"},
		{"prs_darwin_arm64.tar.gz", "def456"},
		{"prs_linux_arm64.tar.gz", "999aaa"}, // leading "*" stripped
		{"prs_windows_amd64.tar.gz", ""},     // not listed
	}
	for _, c := range cases {
		got, err := parseChecksum(sums, c.asset)
		if err != nil || got != c.want {
			t.Errorf("parseChecksum(%q) = (%q,%v), want (%q,nil)", c.asset, got, err, c.want)
		}
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.4", "0.1.4", 0},
		{"v0.1.4", "0.1.4", 0}, // leading v ignored
		{"0.1.3", "0.1.4", -1},
		{"0.1.4", "0.1.3", 1},
		{"0.2.0", "0.1.9", 1},
		{"1.0.0", "0.9.9", 1},
		{"0.1", "0.1.0", 0}, // missing components are 0
		{"0.1", "0.1.1", -1},
		{"dev", "0.1.0", -1}, // non-numeric parses as 0.0.0
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.1.3", "v0.1.4", true},
		{"0.1.4", "v0.1.4", false},
		{"0.1.5", "v0.1.4", false},
		{"dev", "v0.1.4", false}, // dev builds never nag
		{"", "v0.1.4", false},
	}
	for _, c := range cases {
		if got := isNewer(c.current, c.latest); got != c.want {
			t.Errorf("isNewer(%q,%q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestNormalizeAndDisplayVersion(t *testing.T) {
	if got := normalizeTag("0.1.3"); got != "v0.1.3" {
		t.Errorf("normalizeTag(0.1.3) = %q, want v0.1.3", got)
	}
	if got := normalizeTag("v0.1.3"); got != "v0.1.3" {
		t.Errorf("normalizeTag(v0.1.3) = %q, want v0.1.3", got)
	}
	if got := normalizeTag("  "); got != "" {
		t.Errorf("normalizeTag(blank) = %q, want empty", got)
	}
	if got := displayVersion("0.1.4"); got != "v0.1.4" {
		t.Errorf("displayVersion(0.1.4) = %q, want v0.1.4", got)
	}
	if got := displayVersion("dev"); got != "dev" {
		t.Errorf("displayVersion(dev) = %q, want dev", got)
	}
}

// makeTarGz builds an in-memory .tar.gz containing the given name->contents
// entries, for exercising extractPrsBinary.
func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractPrsBinary(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"README": "not the binary",
		"prs":    "BINARY-CONTENTS",
	})
	got, err := extractPrsBinary(bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("extractPrsBinary: %v", err)
	}
	if string(got) != "BINARY-CONTENTS" {
		t.Errorf("extracted %q, want BINARY-CONTENTS", got)
	}

	// No prs entry → error.
	only := makeTarGz(t, map[string]string{"other": "x"})
	if _, err := extractPrsBinary(bytes.NewReader(only)); err == nil {
		t.Error("expected an error when the archive has no prs binary")
	}

	// Not gzip → error.
	if _, err := extractPrsBinary(bytes.NewReader([]byte("plain text"))); err == nil {
		t.Error("expected an error for a non-gzip reader")
	}
}

func TestReplaceExecutable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "prs")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceExecutable(target, []byte("NEW-BINARY")); err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NEW-BINARY" {
		t.Errorf("after replace, contents = %q, want NEW-BINARY", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", info.Mode().Perm())
	}

	// No leftover temp files in the directory (only the target should remain).
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "prs" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir should contain only prs, got %v", names)
	}
}
