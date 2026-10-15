// Command forge is the developer CLI: TUI, daemon, login, setup and the
// start → pr lifecycle.
package main

import (
	"context"
	"os"

	"github.com/hiberus-industria/forge/internal/cli"
)

func main() {
	if err := cli.Execute(context.Background()); err != nil {
		os.Exit(1)
	}
}
