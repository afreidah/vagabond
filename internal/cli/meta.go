// -------------------------------------------------------------------------------
// Shared Command State
//
// Author: Alex Freidah
//
// What every command needs regardless of what it does. Embedded rather than
// passed, so that adding a shared concern does not touch every constructor.
// -------------------------------------------------------------------------------

package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/hashicorp/cli"

	"github.com/afreidah/vagabond/internal/api"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// Meta carries the state shared by every command. Commands print through Ui, so
// a test injects a buffer rather than capturing process streams. Stdin and
// Stdout are held raw for a specification piped in and for output streamed as
// it arrives; ErrStream decides colour and carries the server's log.
type Meta struct {
	Ui        cli.Ui
	Stdin     io.Reader
	Stdout    io.Writer
	ErrStream io.Writer

	address   string
	namespace string
}

// color reports whether diagnostics should carry escape sequences.
func (m *Meta) color() bool {
	return useColor(m.ErrStream)
}

// -------------------------------------------------------------------------
// SERVER CLIENT
// -------------------------------------------------------------------------

// Environment variables supplying -address and -namespace when the flags are
// not given.
const (
	addressEnv   = "VAGABOND_ADDR"
	namespaceEnv = "VAGABOND_NAMESPACE"
)

// clientFlags registers -address and -namespace, which every command that
// talks to a server takes.
func (m *Meta) clientFlags(fs *flag.FlagSet) {
	fs.StringVar(&m.address, "address", os.Getenv(addressEnv), "server address")
	fs.StringVar(&m.namespace, "namespace", os.Getenv(namespaceEnv), "namespace")
}

// connect adds the client flags to fs, parses args, and returns a client for
// -address with the arguments left, or nil once a problem is reported.
func (m *Meta) connect(fs *flag.FlagSet, args []string, want operands, help string) (*api.Client, []string) {
	m.clientFlags(fs)

	got, ok := m.parse(fs, args, want, help)
	if !ok {
		return nil, nil
	}

	client, err := api.NewClient(m.address, nil)
	if err != nil {
		m.Ui.Error(err.Error())

		return nil, nil
	}

	return client, got
}

// -------------------------------------------------------------------------
// ARGUMENTS
// -------------------------------------------------------------------------

// operands is how many arguments a command takes, and how it says so.
type operands struct {
	min, max int
	usage    string
}

// noArgs is a command that takes none.
var noArgs = operands{usage: "no arguments."}

// oneArg is a command that takes exactly the one argument name describes.
func oneArg(name string) operands {
	return operands{min: 1, max: 1, usage: "one argument: " + name}
}

// optionalArg is a command that takes the argument name describes, or none.
func optionalArg(name string) operands {
	return operands{max: 1, usage: "at most one argument: " + name}
}

// parse reads the flags and returns the arguments left, reporting a wrong
// number of them with help. False means the problem has been reported.
func (m *Meta) parse(fs *flag.FlagSet, args []string, want operands, help string) ([]string, bool) {
	if err := fs.Parse(args); err != nil {
		return nil, false
	}

	got := fs.Args()
	if len(got) < want.min || len(got) > want.max {
		m.Ui.Error(fmt.Sprintf("This command takes %s\n\n%s", want.usage, help))

		return nil, false
	}

	return got, true
}

// FlagSet returns a ContinueOnError flag set that reports through the UI, so a
// bad flag returns an exit code rather than calling os.Exit.
func (m *Meta) FlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {}
	fs.SetOutput(uiWriter{ui: m.Ui})

	return fs
}

// -------------------------------------------------------------------------
// OUTPUT
// -------------------------------------------------------------------------

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

// Errorf reports a failure through the UI and returns the failure exit code,
// so that a command can end with a single return statement.
func (m *Meta) Errorf(format string, args ...any) int {
	m.Ui.Error(fmt.Sprintf(format, args...))

	return ExitFailure
}

// columns renders what write prints as aligned, tab-separated columns, with no
// trailing newline.
func columns(write func(w io.Writer)) string {
	var b strings.Builder

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	write(w)
	_ = w.Flush()

	return strings.TrimRight(b.String(), "\n")
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
