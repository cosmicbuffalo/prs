package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

// version is the release version, injected at build time via
// -ldflags "-X main.version=<x.y.z>" from the VERSION file (see the Makefile,
// install.sh, and the release workflow). It stays "dev" for a plain
// `go build`/`go run` with no ldflags.
var version = "dev"

func main() {
	// `prs update [flags] [version]` is a subcommand handled before the normal
	// flag parsing, so its flags don't collide with the TUI's.
	if len(os.Args) > 1 && os.Args[1] == "update" {
		os.Exit(runUpdateCLI(os.Args[2:]))
	}

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: prs [flags]\n       prs update [--check] [--force] [version]\n\nFlags:\n")
		flag.PrintDefaults()
	}
	repo := flag.String("repo", "", "owner/repo override (default: detect from the current git repo)")
	user := flag.String("as_user", "", "GitHub login whose PR activity to view (default: current gh user)")
	showVersion := flag.Bool("version", false, "print the prs version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("prs %s\n", version)
		return
	}

	// Load user config (colors/glyphs, update-check toggle). Best-effort: a
	// missing config is normal and a malformed one shouldn't stop the TUI from
	// opening — warn and fall back to the built-in defaults.
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "prs:", err, "(using defaults)")
	}
	cfg.apply()

	p := tea.NewProgram(NewModel(*repo, *user), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "prs:", err)
		os.Exit(1)
	}
}

// runUpdateCLI parses and runs the `prs update` subcommand, returning a process
// exit code. It accepts --check / --force and an optional positional version
// (e.g. "0.1.3"); if no version is given it honors $PRS_VERSION, else resolves
// the latest release — mirroring install.sh's knobs.
func runUpdateCLI(args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: prs update [--check] [--force] [version]\n\n"+
			"  --check   report whether an update is available, without installing\n"+
			"  --force   reinstall even if not newer (repair, or pin to an older version)\n"+
			"  version   an optional release to install, e.g. 0.1.3 (default: latest)\n")
	}
	check := fs.Bool("check", false, "report whether an update is available, then exit")
	force := fs.Bool("force", false, "reinstall even if the target isn't newer")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	target := fs.Arg(0)
	if target == "" {
		target = os.Getenv("PRS_VERSION")
	}

	if err := runUpdate(version, updateOptions{targetTag: target, check: *check, force: *force}); err != nil {
		fmt.Fprintln(os.Stderr, "prs update:", err)
		return 1
	}
	return 0
}
