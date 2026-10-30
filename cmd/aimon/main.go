// Command aimon is the tray-first, read-only AI/GPU monitor.
package main

import (
	"flag"
	"fmt"
	"os"

	"git.skobk.in/skobkin/aimonitor/internal/app"
	"git.skobk.in/skobkin/aimonitor/internal/buildinfo"
)

func main() {
	// --version must answer before any Fyne/GUI/tray initialization: it
	// works headless and never touches the user session.
	printVersion := flag.Bool("version", false, "print version information and exit")
	flag.Parse()
	if *printVersion {
		fmt.Println(buildinfo.VersionLine())

		return
	}

	// The single-instance guard runs before any Fyne/GUI initialization.
	// A second launch is a user mistake (double-click, restart loop), not
	// an application error, so the loser exits quietly with status zero.
	if err := app.AcquireSingleInstanceLock(); err != nil {
		fmt.Fprintln(os.Stderr, "aimon: another instance is already running")
		os.Exit(0)
	}

	a, err := app.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aimon:", err)
		os.Exit(1)
	}
	a.Run()
}
