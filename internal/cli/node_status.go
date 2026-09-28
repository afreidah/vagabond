// -------------------------------------------------------------------------------
// vagabond node status
//
// Author: Alex Freidah
//
// The client nodes connected to a server now: their pool, what they may use,
// and how many workloads they held when they registered.
// -------------------------------------------------------------------------------

package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"text/tabwriter"
)

// NodeStatusCommand implements `vagabond node status`.
type NodeStatusCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *NodeStatusCommand) Synopsis() string {
	return "List the connected client nodes"
}

// Help returns the full usage text.
func (c *NodeStatusCommand) Help() string {
	text := `
Usage: vagabond node status [options]

  Lists the client nodes connected to the server now, by name. A node that
  loses its connection leaves the list until it reconnects.

Status Options:

  -address <addr>
    The server to read. Defaults to $VAGABOND_ADDR, then
    http://127.0.0.1:4747.
`

	return strings.TrimSpace(text)
}

// Run lists the connected nodes as a table, or says there are none.
func (c *NodeStatusCommand) Run(args []string) int {
	flags := c.FlagSet("node status")
	c.clientFlags(flags)

	if err := flags.Parse(args); err != nil {
		return ExitFailure
	}

	if len(flags.Args()) != 0 {
		return c.Errorf("This command takes no arguments.\n\n%s", c.Help())
	}

	client, err := c.client()
	if err != nil {
		return c.Errorf("%s", err)
	}

	nodes, err := client.Nodes(context.Background())
	if err != nil {
		return c.apiFailure(err)
	}

	if len(nodes) == 0 {
		c.Ui.Output("No client nodes are connected.")

		return ExitSuccess
	}

	var b bytes.Buffer

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tPOOL\tADDRESS\tARCH\tCPU\tMEMORY\tWORKLOADS\tCONNECTED")

	for i := range nodes {
		n := &nodes[i]
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%dm\t%dMiB\t%d\t%s\n",
			n.Name, n.Pool, n.Address, n.Architecture, n.CPU, n.Memory, n.Executions, stamp(n.Connected))
	}

	_ = w.Flush()
	c.Ui.Output(strings.TrimRight(b.String(), "\n"))

	return ExitSuccess
}
