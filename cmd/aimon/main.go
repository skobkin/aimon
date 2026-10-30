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

	a, err := app.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aimon:", err)
		os.Exit(1)
	}
	a.Run()
}
