// Command keyprobe is the standalone key probe, kept so it can be built and run
// without the editor. `raj --probe` is the same code behind a flag.
package main

import (
	"flag"
	"fmt"
	"os"

	"raj/internal/keys"
	"raj/internal/probe"
)

func main() {
	var (
		flags     = flag.Int("flags", 0, "KKP flags to push (0 = raj's own; bit 8 is report_all)")
		useList   = flag.Bool("checklist", false, "walk the canonical binding list")
		configFor = flag.String("config", "", "print the Ghostty config for macos|linux and exit")
	)
	flag.Parse()

	if *configFor != "" {
		fmt.Print(keys.GhosttyConfig(*configFor))
		return
	}
	if err := probe.Run(*flags, *useList); err != nil {
		fmt.Fprintln(os.Stderr, "keyprobe:", err)
		os.Exit(1)
	}
}
