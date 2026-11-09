// -------------------------------------------------------------------------------
// Top-level Help
//
// Author: Alex Freidah
//
// Renders the top-level command listing with each command's synopsis. The
// library's BasicHelpFunc hardcodes double-dash flags in its usage line, which
// contradicts this CLI's single-dash convention.
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

// sortedCommandNames returns the registered commands in a stable order. The
// library passes only the top level, with "job validate" collapsed into "job".
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
