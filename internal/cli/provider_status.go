// -------------------------------------------------------------------------------
// vagabond provider status
//
// Author: Alex Freidah
//
// Every provider the server is configured with: whether it can take work, and
// how much of each budget it has used this period.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/afreidah/vagabond/internal/api"
)

// -------------------------------------------------------------------------
// COMMAND
// -------------------------------------------------------------------------

// ProviderStatusCommand implements `vagabond provider status`.
type ProviderStatusCommand struct {
	*Meta
}

// Synopsis returns the one-line description shown in help listings.
func (c *ProviderStatusCommand) Synopsis() string {
	return "List the providers and what each has used"
}

// Help returns the full usage text.
func (c *ProviderStatusCommand) Help() string {
	text := `
Usage: vagabond provider status [options]

  Lists the providers the server is configured with, whether each is enabled
  and healthy, and how much of each quota pool it has used this period. A
  provider with no pools shows dashes.

  USED counts settled charges plus what running jobs have reserved, at their
  declared resources for their full timeout. It falls when those jobs settle at
  what they actually took, so a running job can make a pool look fuller than it
  will end up.

  These are provider totals across every namespace. No provider is contacted:
  usage comes from the server's last snapshot, whose age is shown.

Status Options:

  -address <addr>
    The server to read. Defaults to $VAGABOND_ADDR, then
    http://127.0.0.1:4747.
`

	return strings.TrimSpace(text)
}

// Run lists the configured providers as a table. A server always has at least
// one, since it refuses to start without.
func (c *ProviderStatusCommand) Run(args []string) int {
	client, _ := c.connect(c.FlagSet("provider status"), args, noArgs, c.Help())
	if client == nil {
		return ExitFailure
	}

	list, err := client.Providers(context.Background())
	if err != nil {
		return c.apiFailure(err)
	}

	c.Ui.Output(columns(func(w io.Writer) {
		_, _ = fmt.Fprintln(w, "NAME\tTIER\tENABLED\tHEALTHY\tPOOL\tMETER\tUSED\tLIMIT\tFREE\tPERIOD")

		for i := range list.Providers {
			providerRows(w, &list.Providers[i])
		}
	}))
	c.Ui.Output("")
	c.Ui.Output(fmt.Sprintf("Usage refreshed %s", stamp(list.Refreshed)))

	return ExitSuccess
}

// -------------------------------------------------------------------------
// RENDERING
// -------------------------------------------------------------------------

// providerRows writes one row per pool, the provider's own columns on the first
// only, or a single row of dashes for a provider with no pools.
func providerRows(w io.Writer, p *api.ProviderListStub) {
	provider := fmt.Sprintf("%s\t%d\t%t\t%t", p.Name, p.Tier, p.Enabled, p.Healthy)

	if len(p.Pools) == 0 {
		_, _ = fmt.Fprintf(w, "%s\t-\t-\t-\t-\t-\t-\n", provider)

		return
	}

	for i := range p.Pools {
		pool := &p.Pools[i]

		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d%%\t%s\n",
			provider, pool.Name, pool.Meter, amount(pool.Used), amount(pool.Limit), pool.FreePercent, pool.Period)

		// Blank, not repeated: the pools below belong to the same provider.
		provider = "\t\t\t"
	}
}

// amount renders a pool figure to one decimal place, leaving whole numbers
// whole.
func amount(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
}
