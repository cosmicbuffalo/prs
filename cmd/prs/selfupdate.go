package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// updateRepo is the GitHub "owner/repo" self-update pulls releases from. It
// mirrors install.sh's REPO so the subcommand and the shell installer always
// agree on where binaries come from.
const updateRepo = "cosmicbuffalo/prs"

// releasesLatestURL is the GitHub API endpoint returning the most recent
// release (used to resolve the "latest" tag).
const releasesLatestURL = "https://api.github.com/repos/" + updateRepo + "/releases/latest"

// updateCheckEnabled gates the background startup update check (see
// checkUpdateCmd). On by default; the config file can turn it off (see
// Config.apply / ReviewConfig's sibling UpdatesConfig).
var updateCheckEnabled = true

// httpTimeout bounds every network call the updater makes so a hung connection
// can't wedge the `prs update` command or leak a background goroutine.
const httpTimeout = 30 * time.Second

// updateOptions controls a single runUpdate invocation.
type updateOptions struct {
	// targetTag pins a specific release (e.g. "v0.1.3" or "0.1.3"); empty means
	// resolve the latest release. Sourced from the positional arg or PRS_VERSION.
	targetTag string
	// check reports whether an update is available without downloading anything.
	check bool
	// force reinstalls even when the target isn't newer than the current build
	// (handy for repair or pinning to an older version).
	force bool
}

// runUpdate implements the `prs update` subcommand. currentVersion is the
// running build's version (main.version). It returns an error describing any
// failure; the caller maps that to an exit code.
func runUpdate(currentVersion string, opts updateOptions) error {
	client := &http.Client{Timeout: httpTimeout}
	ctx := context.Background()

	// Resolve the target tag: an explicit request (arg / PRS_VERSION) wins,
	// else the latest release. An explicit target is always installed — that's
	// how pinning or downgrading works — so the "already up to date" skip below
	// applies only to an implicit "latest" request.
	explicit := normalizeTag(opts.targetTag) != ""
	tag := normalizeTag(opts.targetTag)
	if tag == "" {
		latest, err := fetchLatestTag(ctx, client)
		if err != nil {
			return fmt.Errorf("look up the latest release: %w", err)
		}
		tag = latest
	}

	if opts.check {
		switch {
		case explicit:
			fmt.Printf("`prs update %s` would install %s (current %s)\n", strings.TrimPrefix(tag, "v"), tag, displayVersion(currentVersion))
		case currentVersion != "dev" && compareVersions(currentVersion, tag) >= 0:
			fmt.Printf("prs is up to date (%s)\n", displayVersion(currentVersion))
		default:
			fmt.Printf("update available: %s (current %s) — run `prs update` to install it\n", tag, displayVersion(currentVersion))
		}
		return nil
	}

	// Skip the download only for an implicit "latest" request that's already
	// satisfied (and not forced). Explicit targets always (re)install.
	if !explicit && !opts.force && currentVersion != "dev" && compareVersions(currentVersion, tag) >= 0 {
		fmt.Printf("prs is already up to date (%s)\n", displayVersion(currentVersion))
		return nil
	}

	asset, err := assetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	base := fmt.Sprintf("https://github.com/%s/releases/download/%s", updateRepo, tag)
	fmt.Printf("Downloading %s (%s)\n", asset, tag)
	archive, err := fetchBytes(ctx, client, base+"/"+asset)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}

	// Best-effort checksum verification (mirrors install.sh): verify when the
	// checksums file is available, hard-fail only on an actual mismatch.
	if sums, err := fetchBytes(ctx, client, base+"/checksums.txt"); err == nil {
		expected, perr := parseChecksum(string(sums), asset)
		if perr == nil && expected != "" {
			actual := sha256Hex(archive)
			if actual != expected {
				return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", asset, expected, actual)
			}
			fmt.Println("checksum verified")
		}
	}

	bin, err := extractPrsBinary(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("extract prs from %s: %w", asset, err)
	}

	// Resolve the real on-disk path of the running binary, following any
	// symlink (e.g. ~/.local/bin/prs -> ~/.local/share/prs/bin/prs) so the
	// symlink target is replaced and the link keeps pointing at it.
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate the running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	if err := replaceExecutable(exe, bin); err != nil {
		return err
	}

	fmt.Printf("Updated prs %s → %s (%s)\n", displayVersion(currentVersion), tag, exe)
	return nil
}

// assetName returns the release asset filename for a GOOS/GOARCH pair, matching
// the names the release workflow publishes (prs_<os>_<arch>.tar.gz). It errors
// for any platform without a prebuilt binary so the caller can point the user at
// a source build.
func assetName(goos, goarch string) (string, error) {
	okOS := goos == "linux" || goos == "darwin"
	okArch := goarch == "amd64" || goarch == "arm64"
	if !okOS || !okArch {
		return "", fmt.Errorf("no prebuilt prs binary for %s/%s — build from source (see the README)", goos, goarch)
	}
	return fmt.Sprintf("prs_%s_%s.tar.gz", goos, goarch), nil
}

// parseLatestTag pulls tag_name out of a GitHub "releases/latest" API response.
func parseLatestTag(body []byte) (string, error) {
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", fmt.Errorf("parse release JSON: %w", err)
	}
	if rel.TagName == "" {
		return "", fmt.Errorf("no tag_name in release response")
	}
	return rel.TagName, nil
}

// parseChecksum finds the sha256 hex for asset in the contents of a
// checksums.txt file (each line is "<hash>  <filename>", as produced by
// sha256sum). Returns "" (no error) when the asset isn't listed.
func parseChecksum(checksums, asset string) (string, error) {
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// The filename may carry a leading "*" (binary-mode marker from some
		// sha256sum implementations); strip it before comparing.
		name := strings.TrimPrefix(fields[1], "*")
		if name == asset {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", nil
}

// compareVersions compares two dotted numeric versions (a leading "v" is
// ignored, missing components count as 0), returning -1 if a < b, 0 if equal,
// and 1 if a > b. Non-numeric components sort as 0, so "dev" compares as 0.0.0.
func compareVersions(a, b string) int {
	pa := versionParts(a)
	pb := versionParts(b)
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}

// versionParts splits a version like "v0.1.4" into [0,1,4]; non-numeric parts
// become 0.
func versionParts(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil
	}
	segs := strings.Split(v, ".")
	out := make([]int, len(segs))
	for i, s := range segs {
		n, err := strconv.Atoi(s)
		if err != nil {
			n = 0
		}
		out[i] = n
	}
	return out
}

// isNewer reports whether latest is a strictly newer release than the running
// build. A "dev" (unversioned) build always reports false, so local/dev builds
// never nag about updates.
func isNewer(current, latest string) bool {
	if current == "" || current == "dev" {
		return false
	}
	return compareVersions(latest, current) > 0
}

// normalizeTag turns a user-supplied version ("0.1.3", "v0.1.3", "  0.1.3 ")
// into the tag form the release uses ("v0.1.3"); "" stays "".
func normalizeTag(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}

// displayVersion renders a version for user-facing messages: a real version is
// shown with a leading "v", a dev build as "dev".
func displayVersion(v string) string {
	if v == "" || v == "dev" {
		return "dev"
	}
	return "v" + strings.TrimPrefix(v, "v")
}

// extractPrsBinary reads a .tar.gz release archive and returns the bytes of the
// "prs" entry at its root.
func extractPrsBinary(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		// Match the plain "prs" file at the archive root (the release tars it as
		// just "prs"); ignore any nested paths.
		if hdr.Typeflag == tar.TypeReg && filepath.Base(hdr.Name) == "prs" && !strings.ContainsRune(strings.TrimSuffix(hdr.Name, "prs"), '/') {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read prs from archive: %w", err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("archive did not contain a prs binary")
}

// replaceExecutable atomically swaps the file at path with data. It writes a
// temp file in the same directory (so the final os.Rename is atomic and stays
// on one filesystem), makes it executable, then renames it over path. On Linux
// and macOS this is safe to do to a running binary: the live process keeps the
// old inode, and future launches get the new file.
func replaceExecutable(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".prs-update-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s (is it writable?): %w", dir, err)
	}
	tmpPath := tmp.Name()

	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close new binary: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		cleanup()
		return fmt.Errorf("chmod new binary: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("replace %s (try re-running with sudo, or re-run the installer): %w", path, err)
	}
	return nil
}

// fetchLatestTag resolves the latest release's tag via the GitHub API.
func fetchLatestTag(ctx context.Context, client *http.Client) (string, error) {
	body, err := fetchBytes(ctx, client, releasesLatestURL)
	if err != nil {
		return "", err
	}
	return parseLatestTag(body)
}

// fetchBytes GETs url and returns the body, erroring on any non-200 status.
func fetchBytes(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "prs-self-update")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// sha256Hex returns the lowercase hex sha256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// updateCheckInterval is how long a cached "latest release" lookup is trusted
// before the background startup check hits the network again — so launching prs
// repeatedly doesn't spam the GitHub API (and works offline in between).
const updateCheckInterval = 24 * time.Hour

// updateCache is the on-disk shape of the startup update check's cache
// (update-check.json in stateBaseDir()).
type updateCache struct {
	CheckedAt time.Time `json:"checked_at"`
	LatestTag string    `json:"latest_tag"`
}

// updateCachePath returns stateBaseDir()/update-check.json.
func updateCachePath() (string, error) {
	dir, err := stateBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "update-check.json"), nil
}

// readUpdateCache loads the cached latest-release lookup, if present and
// parseable (best-effort — any problem is treated as "no cache").
func readUpdateCache() (updateCache, bool) {
	path, err := updateCachePath()
	if err != nil {
		return updateCache{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return updateCache{}, false
	}
	var c updateCache
	if err := json.Unmarshal(data, &c); err != nil {
		return updateCache{}, false
	}
	return c, true
}

// writeUpdateCache persists the latest-release lookup (best-effort; the dir is
// created if needed and errors are returned for the caller to ignore).
func writeUpdateCache(c updateCache) error {
	path, err := updateCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// latestTagCached resolves the latest release tag for the startup check, using
// the cache when it's fresher than updateCheckInterval and otherwise fetching
// and re-caching. now is passed in for testability. It never blocks longer than
// the HTTP timeout, so it's safe to run from a background tea.Cmd.
func latestTagCached(now time.Time) (string, error) {
	if c, ok := readUpdateCache(); ok && c.LatestTag != "" && now.Sub(c.CheckedAt) < updateCheckInterval {
		return c.LatestTag, nil
	}
	client := &http.Client{Timeout: httpTimeout}
	tag, err := fetchLatestTag(context.Background(), client)
	if err != nil {
		return "", err
	}
	// Best-effort cache write — a failure here just means we re-check sooner.
	_ = writeUpdateCache(updateCache{CheckedAt: now, LatestTag: tag})
	return tag, nil
}
