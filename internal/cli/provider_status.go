// -------------------------------------------------------------------------------
// vagabond provider status
//
// Author: Alex Freidah
//
// Every provider the server is configured with, one row each in the order work
// goes to them: whether it can take work and how much of each budget it has
// left. Given a name, one provider's budgets in full.
// -------------------------------------------------------------------------------

package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

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
	return "List the providers and what each has left"
}

// Help returns the full usage text.
func (c *ProviderStatusCommand) Help() string {
	text := `
Usage: vagabond provider status [options] [name]

  Lists the providers the server is configured with, one row each, by tier and
  then name: whether each can take work, and the share of each quota pool it
  has left this period. Given a name, shows that provider's pools in full.

  Usage counts settled charges plus what running jobs have reserved, at their
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

// Run lists the configured providers, or shows the one named. A server always
// has at least one, since it refuses to start without.
func (c *ProviderStatusCommand) Run(args []string) int {
	client, names := c.connect(c.FlagSet("provider status"), args, optionalArg("[name]"), c.Help())
	if client == nil {
		return ExitFailure
	}

	list, err := client.Providers(context.Background())
	if err != nil {
		return c.apiFailure(err)
	}

	if len(names) == 0 {
		c.Ui.Output(providerTable(list.Providers))
	} else {
		p := findProvider(list.Providers, names[0])
		if p == nil {
			return c.Errorf("Error: no provider is named %q. Configured: %s.",
				names[0], strings.Join(providerNames(list.Providers), ", "))
		}

		c.Ui.Output(providerDetail(p))
	}

	c.Ui.Output("")
	c.Ui.Output(usageAge(list.Refreshed, time.Now()))

	return ExitSuccess
}

// -------------------------------------------------------------------------
// LIST
// -------------------------------------------------------------------------

// providerTable renders one row per provider, by tier and then name, which is
// the order work goes to them.
func providerTable(providers []api.ProviderListStub) string {
	sorted := slices.Clone(providers)
	slices.SortFunc(sorted, func(a, b api.ProviderListStub) int {
		return cmp.Or(cmp.Compare(a.Tier, b.Tier), strings.Compare(a.Name, b.Name))
	})

	return columns(func(w io.Writer) {
		_, _ = fmt.Fprintln(w, "PROVIDER\tTIER\tSTATUS\tQUOTA LEFT")

		for i := range sorted {
			p := &sorted[i]
			_, _ = fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", p.Name, p.Tier, providerState(p), quotaLeft(p.Pools))
		}
	})
}

// providerState is whether a provider can take work: disabled by its
// configuration, unhealthy at its last check, or healthy.
func providerState(p *api.ProviderListStub) string {
	switch {
	case !p.Enabled:
		return "disabled"
	case !p.Healthy:
		return "unhealthy"
	default:
		return "healthy"
	}
}

// quotaLeft names each pool with the share of it left, so the one that rules
// the provider out first reads at a glance.
func quotaLeft(pools []api.ProviderPool) string {
	if len(pools) == 0 {
		return "no quota"
	}

	parts := make([]string, 0, len(pools))
	for i := range pools {
		parts = append(parts, fmt.Sprintf("%s %d%%", pools[i].Name, pools[i].FreePercent))
	}

	return strings.Join(parts, ", ")
}

// -------------------------------------------------------------------------
// DETAIL
// -------------------------------------------------------------------------

// findProvider returns the provider with the given name, nil when there is
// none.
func findProvider(providers []api.ProviderListStub, name string) *api.ProviderListStub {
	for i := range providers {
		if providers[i].Name == name {
			return &providers[i]
		}
	}

	return nil
}

// providerNames lists the configured providers' names, for the error naming
// one that is not among them.
func providerNames(providers []api.ProviderListStub) []string {
	names := make([]string, 0, len(providers))
	for i := range providers {
		names = append(names, providers[i].Name)
	}

	slices.Sort(names)

	return names
}

// providerDetail renders one provider: its fields, then a row per pool with
// whole figures in the pool's own unit.
func providerDetail(p *api.ProviderListStub) string {
	var out strings.Builder

	_, _ = fmt.Fprintf(&out, "Name   = %s\nTier   = %d\nStatus = %s\n\n", p.Name, p.Tier, providerState(p))

	if len(p.Pools) == 0 {
		out.WriteString("No quota pools.")

		return out.String()
	}

	out.WriteString(columns(func(w io.Writer) {
		_, _ = fmt.Fprintln(w, "POOL\tUSED\tLIMIT\tUNIT\tLEFT\tPERIOD")

		for i := range p.Pools {
			pool := &p.Pools[i]
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d%%\t%s\n",
				pool.Name, whole(pool.Used), whole(pool.Limit), pool.Unit, pool.FreePercent, pool.Period)
		}
	}))

	return out.String()
}

// -------------------------------------------------------------------------
// FORMATTING
// -------------------------------------------------------------------------

// whole renders a pool figure rounded to a whole number, with thousands
// separators.
func whole(v float64) string {
	n := int64(math.Round(v))

	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}

	digits := strconv.FormatInt(n, 10)

	var b strings.Builder

	b.WriteString(sign)

	for i, d := range digits {
		// A separator before every group of three counted from the right
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}

		b.WriteRune(d)
	}

	return b.String()
}

// usageAge says when the usage shown was read, and how long before now.
func usageAge(refreshed, now time.Time) string {
	if refreshed.IsZero() {
		return "Usage has not been read yet."
	}

	age := max(now.Sub(refreshed), 0).Round(time.Second)

	return fmt.Sprintf("Usage as of %s (%s ago).", stamp(refreshed), age)
}
