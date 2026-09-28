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

// Commands returns the command set, keyed by the words a user types. Every
// command but job validate, server and agent talks to a server's API.
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
		"job register": func() (cli.Command, error) {
			return &JobRegisterCommand{Meta: meta}, nil
		},
		"job dispatch": func() (cli.Command, error) {
			return &JobDispatchCommand{Meta: meta}, nil
		},
		"job status": func() (cli.Command, error) {
			return &JobStatusCommand{Meta: meta}, nil
		},
		"job stop": func() (cli.Command, error) {
			return &JobStopCommand{Meta: meta}, nil
		},
		"agent": func() (cli.Command, error) {
			return &AgentCommand{Meta: meta}, nil
		},
		"node": func() (cli.Command, error) {
			return &NodeCommand{Meta: meta}, nil
		},
		"node status": func() (cli.Command, error) {
			return &NodeStatusCommand{Meta: meta}, nil
		},
		"execution": func() (cli.Command, error) {
			return &ExecutionCommand{Meta: meta}, nil
		},
		"execution status": func() (cli.Command, error) {
			return &ExecutionStatusCommand{Meta: meta}, nil
		},
		"execution logs": func() (cli.Command, error) {
			return &ExecutionLogsCommand{Meta: meta}, nil
		},
		"server": func() (cli.Command, error) {
			return &ServerCommand{Meta: meta}, nil
		},
	}
}
