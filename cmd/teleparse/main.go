// Command teleparse is a Telegram userbot media parser CLI.
package main

import (
	"os"

	"github.com/4q4r/teleparse/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
