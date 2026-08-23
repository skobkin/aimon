// Command aimon is the tray-first, read-only AI/GPU monitor.
package main

import (
	"fmt"
	"os"

	"git.skobk.in/skobkin/aimonitor/internal/app"
)

func main() {
	a, err := app.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "aimon:", err)
		os.Exit(1)
	}
	a.Run()
}
