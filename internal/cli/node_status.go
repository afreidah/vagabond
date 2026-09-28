// -------------------------------------------------------------------------------
// vagabond node status
//
// Author: Alex Freidah
//
// The agent nodes connected to a server now: their pool, what they may use,
// and how much of it their running workloads have taken.
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
	return "List the connected agent nodes"
}

// Help returns the full usage text.
func (c *NodeStatusCommand) Help() string {
	text := `
Usage: vagabond node status [options]

  Lists the agent nodes connected to the server now, by name, with what each
  may use and how much its running workloads have taken. A node that loses its
  connection leaves the list until it reconnects.

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
		c.Ui.Output("No agent nodes are connected.")

		return ExitSuccess
	}

	var b bytes.Buffer

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tPOOL\tADDRESS\tARCH\tCPU USED\tMEMORY USED\tWORKLOADS\tCONNECTED")

	for i := range nodes {
		n := &nodes[i]
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d/%dm\t%d/%dMiB\t%d\t%s\n",
			n.Name, n.Pool, n.Address, n.Architecture,
			n.UsedCPU, n.CPU, n.UsedMemory, n.Memory, n.Executions, stamp(n.Connected))
	}

	_ = w.Flush()
	c.Ui.Output(strings.TrimRight(b.String(), "\n"))

	return ExitSuccess
}
