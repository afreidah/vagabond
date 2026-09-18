// Package cli implements the vagabond command line.
//
// The command surface is deliberately Nomad-shaped, down to single-dash flags,
// because a Nomad user is the person this tool is for. That is why it is built
// on github.com/hashicorp/cli rather than cobra: cobra brings pflag and pushes
// toward --meta, and at that point the CLI stops reading like the thing it is
// an homage to.
//
// Commands write through a cli.Ui rather than printing directly, so that a
// test can inject a buffer and assert on output instead of capturing streams.
// Nothing in here decides anything: a command parses flags, calls into the
// packages that hold the logic, and turns the answer into text and an exit
// code.
package cli
