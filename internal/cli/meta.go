// -------------------------------------------------------------------------------
// Shared Command State
//
// Author: Alex Freidah
//
// What every command needs regardless of what it does. Embedded rather than
// passed, following Nomad's command.Meta, so that adding a shared concern later
// does not mean touching every constructor.
// -------------------------------------------------------------------------------

package cli

import (
	"flag"
	"fmt"
	"strings"

	"github.com/hashicorp/cli"
)

// Meta carries the state shared by every command.
//
// Ui is the only member for now. It exists so that commands never print
// directly: a test injects a buffer and reads what the command said, rather
// than capturing process streams and hoping nothing else wrote to them.
type Meta struct {
	Ui cli.Ui
}

// FlagSet returns a flag set that reports errors through the UI rather than
// writing to os.Stderr behind the command's back.
//
// ContinueOnError rather than ExitOnError, because a flag package that calls
// os.Exit makes the command untestable and takes the exit code decision away
// from the caller.
func (m *Meta) FlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {}
	fs.SetOutput(uiWriter{ui: m.Ui})

	return fs
}

// Errorf reports a failure through the UI and returns the failure exit code,
// so that a command can end with a single return statement.
func (m *Meta) Errorf(format string, args ...any) int {
	m.Ui.Error(fmt.Sprintf(format, args...))

	return ExitFailure
}

// uiWriter adapts a cli.Ui to io.Writer so the flag package can report through
// it.
type uiWriter struct {
	ui cli.Ui
}

// Write sends a line to the UI's error stream, trimming the trailing newline
// the flag package adds, since the UI adds its own.
func (w uiWriter) Write(p []byte) (int, error) {
	w.ui.Error(strings.TrimRight(string(p), "\n"))

	return len(p), nil
}
