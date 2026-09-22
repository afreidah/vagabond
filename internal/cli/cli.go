// -------------------------------------------------------------------------------
// CLI Entry Point
//
// Author: Alex Freidah
//
// Builds the command tree and runs it. The binary's main is a wrapper around
// Run so that the exit code is a return value rather than an os.Exit buried in
// a package nothing can test.
//
// hashicorp/cli registers commands in a flat map whose keys carry spaces, so
// "job validate" is one key rather than a child of "job". The library derives
// the namespace from that spelling on its own, which is why no custom help
// function is needed here: "vagabond job" lists its subcommands without being
// registered.
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

// Exit codes. These are an interface the moment a CI system runs job validate,
// so they are fixed here and used everywhere rather than invented per command.
//
// Failure covers usage errors too, because a caller cannot act differently on
// those. NoCapacity is separate because a caller can: a failing build is the
// author's problem, and nowhere to run it is an infrastructure one.
const (
	ExitSuccess    = 0 // the command did what was asked
	ExitFailure    = 1 // it ran and the answer was no
	ExitNoCapacity = 2 // the work never ran
)

const binaryName = "vagabond"

// -------------------------------------------------------------------------
// ENTRY POINT
// -------------------------------------------------------------------------

// Run executes the command named in args and returns the process exit code.
//
// Takes its streams rather than reaching for os.Stdout so that the whole CLI
// can be exercised from a test.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	ui := &cli.BasicUi{
		Reader:      stdin,
		Writer:      stdout,
		ErrorWriter: stderr,
	}

	meta := &Meta{Ui: ui, Stdin: stdin, ErrStream: stderr}

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

// normalizeExit collapses the library's own codes into the two this CLI
// documents.
//
// hashicorp/cli returns 127 when no command matched, which conventionally means
// "command not found" at a shell and would be read as the binary being missing
// rather than the argument being wrong. A caller branching on our exit code
// should only ever see success or failure.
func normalizeExit(code int) int {
	if code == ExitSuccess {
		return ExitSuccess
	}

	return ExitFailure
}
