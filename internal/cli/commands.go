// -------------------------------------------------------------------------------
// Command Registry
//
// Author: Alex Freidah
//
// Every command the binary answers to, in one map. Keys carry spaces because
// hashicorp/cli has no command tree: "job validate" is a key, not a child of
// "job", and the grouped help function in cli.go is what makes that read like a
// hierarchy.
// -------------------------------------------------------------------------------

package cli

import "github.com/hashicorp/cli"

// Commands returns the command set, keyed by the words a user types.
//
// The README's target surface is job validate, plan, run, and status. Run and
// status are absent, because a command that exists and refuses to work is
// worse than one that does not exist: the first looks like a bug and the
// second looks like a roadmap.
func Commands(meta *Meta) map[string]cli.CommandFactory {
	return map[string]cli.CommandFactory{
		"job": func() (cli.Command, error) {
			return &JobCommand{Meta: meta}, nil
		},
		"job validate": func() (cli.Command, error) {
			return &JobValidateCommand{Meta: meta}, nil
		},
		"job plan": func() (cli.Command, error) {
			return &JobPlanCommand{Meta: meta}, nil
		},
		"job run": func() (cli.Command, error) {
			return &JobRunCommand{Meta: meta}, nil
		},
	}
}
