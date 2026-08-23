// Command llm-monitor is the tray-first, read-only LLM/GPU monitor.
package main

import (
	"fmt"
	"os"

	"github.com/skobkin/llm-monitor/internal/app"
)

func main() {
	a, err := app.New()
	if err != nil {
		fmt.Fprintln(os.Stderr, "llm-monitor:", err)
		os.Exit(1)
	}
	a.Run()
}
