// -------------------------------------------------------------------------------
// vagabond - command line entry point
//
// Author: Alex Freidah
//
// Deliberately thin. Everything worth testing lives in internal/cli, which
// returns an exit code rather than calling os.Exit, so the whole command
// surface can be exercised from a test without building a binary.
// -------------------------------------------------------------------------------

package main

import (
	"os"

	"github.com/afreidah/vagabond/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
