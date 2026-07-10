package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	repo := flag.String("repo", "", "owner/repo override (default: detect from the current git repo)")
	user := flag.String("as_user", "", "GitHub login whose PR activity to view (default: current gh user)")
	flag.Parse()

	p := tea.NewProgram(NewModel(*repo, *user), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "prs:", err)
		os.Exit(1)
	}
}
