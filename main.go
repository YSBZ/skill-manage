// Command skillmanage is a single-binary daemon that tracks git skill repos,
// keeps them fresh, and links selected skills into Claude Code's skill
// directories. This entrypoint is scaffolding (U2); the daemon loop, HTTP
// server, scheduler, and link engine land in later units.
package main

import (
	"flag"
	"fmt"
	"os"

	"skillmanage/internal/config"
)

func main() {
	centralDir := flag.String("central", "", "central folder (default ~/.skillmanage)")
	flag.Parse()

	dir := *centralDir
	if dir == "" {
		d, err := config.DefaultCentralDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "skillmanage:", err)
			os.Exit(1)
		}
		dir = d
	}

	cfg, firstRun, err := config.LoadConfig(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "skillmanage:", err)
		os.Exit(1)
	}

	if firstRun {
		fmt.Printf("skillmanage: first run — central folder %s not yet configured\n", dir)
		return
	}
	fmt.Printf("skillmanage: central folder %s, %d repo(s), %d enabled entr(ies)\n",
		dir, len(cfg.Repos), len(cfg.Enabled))
}
