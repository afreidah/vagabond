// -------------------------------------------------------------------------------
// vagabond provider disable / enable
//
// Author: Alex Freidah
//
// Turns a provider off and on through the API, for every server sharing the
// store. One command type serves both: enable picks the call and the help.
// -------------------------------------------------------------------------------

package cli

import (
	"context"
	"fmt"
)

// -------------------------------------------------------------------------
// HELP
// -------------------------------------------------------------------------

const (
	providerDisableHelp = `
Usage: vagabond provider disable [options] <name>

  Stops new work going to a provider, on every server sharing the store.
  Executions already running on it are left to finish. A plan rejects it as
  provider-disabled, saying when it was disabled and by whom: the name of
  your token or, on a server without tokens, the common name on your client
  certificate. Needs a management token on a server that declares tokens.

  The provider stays disabled across restarts until it is enabled again.
  Disabling it again keeps the first time and name. Servers other than the
  one asked see the change within 5 seconds.
`

	providerEnableHelp = `
Usage: vagabond provider enable [options] <name>

  Lets new work go to a provider that vagabond provider disable turned off, on
  every server sharing the store.

  A provider turned off with enabled = false in the configuration file is
  refused, since only the file can turn it back on. Needs a management token
  on a server that declares tokens.
`
)

// -------------------------------------------------------------------------
// COMMAND
// -------------------------------------------------------------------------

// ProviderSwitchCommand implements `vagabond provider disable` and, with
// enable set, `vagabond provider enable`.
type ProviderSwitchCommand struct {
	*Meta
	enable bool
}

// Synopsis returns the one-line description shown in help listings, for
// whichever of the two this command is.
func (c *ProviderSwitchCommand) Synopsis() string {
	if c.enable {
		return "Let new work go to a disabled provider"
	}

	return "Stop new work going to a provider"
}

// Help returns the full usage text, for whichever of the two this command
// is.
func (c *ProviderSwitchCommand) Help() string {
	if c.enable {
		return clientHelp(providerEnableHelp)
	}

	return clientHelp(providerDisableHelp)
}

// Run turns the named provider off, or on when enable is set, and says how it
// stands afterwards.
func (c *ProviderSwitchCommand) Run(args []string) int {
	verb := "disable"
	if c.enable {
		verb = "enable"
	}

	client, names := c.connect(c.FlagSet("provider "+verb), args, oneArg("<name>"), c.Help())
	if client == nil {
		return ExitFailure
	}

	turn := client.DisableProvider
	if c.enable {
		turn = client.EnableProvider
	}

	p, err := turn(context.Background(), names[0])
	if err != nil {
		return c.apiFailure(err)
	}

	c.Ui.Output(fmt.Sprintf("Provider %q is now %s.", p.Name, providerState(p)))

	return ExitSuccess
}
