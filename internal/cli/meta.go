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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/hashicorp/cli"

	"github.com/afreidah/vagabond/internal/api"
)

// Meta carries the state shared by every command.
//
// Ui exists so that commands never print directly: a test injects a buffer and
// reads what the command said, rather than capturing process streams and hoping
// nothing else wrote to them.
//
// Stdin is held separately because a command reading a specification from a
// pipe needs the reader itself, not the line-oriented prompting a cli.Ui
// offers. ErrStream is held only to ask whether it is a terminal, which decides
// colour, and for the server's log.
//
// address and namespace are set by clientFlags for commands that talk to a
// server.
type Meta struct {
	Ui        cli.Ui
	Stdin     io.Reader
	ErrStream io.Writer

	address   string
	namespace string
}

// Environment variables supplying -address and -namespace when the flags are
// not given.
const (
	addressEnv   = "VAGABOND_ADDR"
	namespaceEnv = "VAGABOND_NAMESPACE"
)

// color reports whether diagnostics should carry escape sequences.
func (m *Meta) color() bool {
	return useColor(m.ErrStream)
}

// -------------------------------------------------------------------------
// SERVER CLIENT
// -------------------------------------------------------------------------

// clientFlags registers -address and -namespace, which every command that
// talks to a server takes.
func (m *Meta) clientFlags(fs *flag.FlagSet) {
	fs.StringVar(&m.address, "address", os.Getenv(addressEnv), "server address")
	fs.StringVar(&m.namespace, "namespace", os.Getenv(namespaceEnv), "namespace")
}

// client returns a client for the server -address names.
func (m *Meta) client() (*api.Client, error) {
	return api.NewClient(m.address, nil)
}

// apiFailure reports an error from the server and returns the failure exit
// code. A job the server found invalid is reported one diagnostic at a time.
func (m *Meta) apiFailure(err error) int {
	var failure *api.ResponseError
	if !errors.As(err, &failure) || len(failure.Body.Diagnostics) == 0 {
		return m.Errorf("Error: %s", err)
	}

	for _, d := range failure.Body.Diagnostics {
		line := fmt.Sprintf("Error: %s", d.Summary)
		if d.Range != "" {
			line += fmt.Sprintf("\n\n  on %s", d.Range)
		}

		if d.Detail != "" {
			line += "\n\n" + d.Detail
		}

		m.Ui.Error(line)
	}

	return ExitFailure
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
