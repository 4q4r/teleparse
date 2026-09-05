// Command teleparse is a Telegram userbot media parser CLI.
package main

import (
	"fmt"
	"os"

	"github.com/4q4r/teleparse/internal/cli"
)

func main() {
	if err := cli.New().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
