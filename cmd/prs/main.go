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
	repo := flag.String("repo", "", "owner/repo override (default: detect from the current git repo)")
	user := flag.String("as_user", "", "GitHub login whose PR activity to view (default: current gh user)")
	showVersion := flag.Bool("version", false, "print the prs version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("prs %s\n", version)
		return
	}

	p := tea.NewProgram(NewModel(*repo, *user), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "prs:", err)
		os.Exit(1)
	}
}
