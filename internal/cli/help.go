// -------------------------------------------------------------------------------
// Top-level Help
//
// Author: Alex Freidah
//
// The library's BasicHelpFunc hardcodes double-dash flags in its usage line,
// which contradicts the single-dash convention this CLI exists to match. Nomad
// carries its own help function for the same reason.
//
// Listing subcommands with their synopses rather than only the namespace is the
// other reason: "job" on its own tells a reader nothing about what they can do
// with it.
// -------------------------------------------------------------------------------

package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/cli"
)

// helpFunc renders the top-level command listing.
func helpFunc(commands map[string]cli.CommandFactory) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Usage: %s [-version] [-help] <command> [<args>]\n\n", binaryName)
	b.WriteString("Available commands are:\n")

	for _, name := range sortedCommandNames(commands) {
		fmt.Fprintf(&b, "    %-16s %s\n", name, synopsis(commands, name))
	}

	return b.String()
}

// sortedCommandNames returns the registered commands in a stable order.
//
// The library hands this function only the top level: "job validate" has
// already been collapsed into "job" by the time help is rendered, which is why
// the namespace is registered as a real command with its own synopsis rather
// than left as a synthesised placeholder.
func sortedCommandNames(commands map[string]cli.CommandFactory) []string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// synopsis returns a command's one-line description, or an empty string when
// the command cannot be constructed. A help listing is not the place to fail.
func synopsis(commands map[string]cli.CommandFactory, name string) string {
	factory, ok := commands[name]
	if !ok {
		return ""
	}

	command, err := factory()
	if err != nil {
		return ""
	}

	return command.Synopsis()
}
