// -------------------------------------------------------------------------------
// CLI Entry Point
//
// Author: Alex Freidah
//
// Builds the command tree and runs it. The binary's main is a wrapper around
// Run so that the exit code is a return value rather than an os.Exit buried in
// a package nothing can test.
// -------------------------------------------------------------------------------

package cli

import (
	"fmt"
	"io"

	"github.com/hashicorp/cli"

	"github.com/afreidah/vagabond/internal/version"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// Exit codes are an interface once CI branches on them, so they are fixed here
// rather than invented per command. Failure covers usage errors too.
// NoCapacity is separate because a caller can act on it: a failing build is
// the author's problem, and nowhere to run it is an infrastructure one.
const (
	ExitSuccess    = 0 // the command did what was asked
	ExitFailure    = 1 // it ran and the answer was no
	ExitNoCapacity = 2 // the work never ran, or was interrupted
)

const binaryName = "vagabond"

// -------------------------------------------------------------------------
// ENTRY POINT
// -------------------------------------------------------------------------

// Run executes the command named in args and returns the process exit code. It
// takes its streams rather than os.Stdout so the whole CLI runs from a test.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ui := &cli.BasicUi{
		Reader:      stdin,
		Writer:      stdout,
		ErrorWriter: stderr,
	}

	meta := &Meta{Ui: ui, Stdin: stdin, Stdout: stdout, ErrStream: stderr}

	app := &cli.CLI{
		Name:                       binaryName,
		Version:                    version.String(),
		Args:                       args,
		Commands:                   Commands(meta),
		Autocomplete:               true,
		AutocompleteNoDefaultFlags: true,
		HelpFunc:                   helpFunc,
		HelpWriter:                 stdout,
		ErrorWriter:                stderr,
	}

	code, err := app.Run()
	if err != nil {
		fmt.Fprintf(stderr, "Error executing CLI: %s\n", err)

		return ExitFailure
	}

	return normalizeExit(code)
}

// normalizeExit collapses undocumented codes to failure, such as the 127 that
// hashicorp/cli returns for an unknown command and a shell reads as missing.
func normalizeExit(code int) int {
	switch code {
	case ExitSuccess, ExitFailure, ExitNoCapacity:
		return code
	default:
		return ExitFailure
	}
}
