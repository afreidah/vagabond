// Package cli implements the vagabond command line.
//
// The command surface follows Nomad's, down to single-dash flags, so it reads
// familiarly to the operators it is built for. That is why it uses
// github.com/hashicorp/cli rather than cobra, whose pflag pushes toward
// double-dash flags.
//
// Commands write through a cli.Ui so a test can inject a buffer and assert on
// output. A client command decides nothing: it parses flags, validates a job
// file locally, calls the server through internal/api, and turns the answer
// into text and an exit code. server and agent run their logic in-process.
package cli
