// -------------------------------------------------------------------------------
// Task to Job Run
//
// Author: Alex Freidah
//
// Turning a normalized task into what Code Engine wants. Three settings matter
// most: retries, because Code Engine's default would multiply a failing run
// behind the ledger's back; the size, because Code Engine runs only fixed vCPU
// and memory pairs; and the service account, because the default hands the
// workload an identity inside the project. The command line and environment
// are escaped and carried so the container receives them as the task wrote them.
// -------------------------------------------------------------------------------

package ibm

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// What Code Engine accepts, published through Capabilities. CPU is millicores
// and memory MiB to match job.Resources: 48 GB, the largest memory, is 45776
// MiB.
const (
	maxCPU      = 12000
	maxMemory   = 45776
	maxDuration = 24 * time.Hour
)

// Defaults for a task that states nothing, the same as Cloud Run's.
const (
	defaultCPU    = 1000
	defaultMemory = 2048
)

// The variables carrying a command or argument that holds a line break.
// VAGABOND_ARG_<i> counts from zero, as args does.
const (
	carriedCommand   = "VAGABOND_COMMAND"
	carriedArgPrefix = "VAGABOND_ARG_"
)

// bytesPerMiB and bytesPerGB convert between Vagabond's binary MiB and Code
// Engine's decimal G.
const (
	bytesPerMiB = 1 << 20
	bytesPerGB  = 1e9
)

// sizes are the vCPU and memory pairs Code Engine runs, in order of CPU and
// then memory, so the first that fits a task is the smallest.
var sizes = []size{
	{125, 0.25}, {125, 0.5}, {125, 1},
	{250, 0.5}, {250, 1}, {250, 2},
	{500, 1}, {500, 2}, {500, 4},
	{1000, 2}, {1000, 4}, {1000, 8},
	{2000, 4}, {2000, 8}, {2000, 16},
	{4000, 8}, {4000, 16}, {4000, 32},
	{6000, 12}, {6000, 24}, {6000, 48},
	{8000, 16}, {8000, 32},
	{10000, 20}, {10000, 40},
	{12000, 24}, {12000, 48},
}

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// jobRunSpec is the body of a job-run submission. RetryLimit has no omitempty:
// zero is the point, and an absent field means Code Engine's default of 3.
type jobRunSpec struct {
	Name             string   `json:"name"`
	Image            string   `json:"image_reference"`
	ImageSecret      string   `json:"image_secret,omitempty"`
	Commands         []string `json:"run_commands,omitempty"`
	Arguments        []string `json:"run_arguments,omitempty"`
	Env              []envVar `json:"run_env_variables,omitempty"`
	Mode             string   `json:"run_mode"`
	ServiceAccount   string   `json:"run_service_account"`
	CPU              string   `json:"scale_cpu_limit"`
	Memory           string   `json:"scale_memory_limit"`
	MaxExecutionTime int      `json:"scale_max_execution_time"`
	RetryLimit       int      `json:"scale_retry_limit"`
	ArraySpec        string   `json:"scale_array_spec"`
}

// envVar is one literal environment variable.
type envVar struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// size is one vCPU and memory pair, in millicores and decimal GB.
type size struct {
	millicores int
	gb         float64
}

// -------------------------------------------------------------------------
// THE SPEC
// -------------------------------------------------------------------------

// buildSpec builds the job run for a task under the given name.
func (p *Provider) buildSpec(name string, task *job.Task) (*jobRunSpec, error) {
	image, ok := task.ConfigString(job.ConfigImage)
	if !ok {
		return nil, plugin.Internal(fmt.Errorf("task %q names no image", task.Name))
	}

	fit, err := sizeFor(task)
	if err != nil {
		return nil, plugin.Internal(err)
	}

	seconds, err := executionSeconds(task)
	if err != nil {
		return nil, plugin.Internal(err)
	}

	env, err := environment(task)
	if err != nil {
		return nil, err
	}

	spec := &jobRunSpec{
		Name:             name,
		Image:            image,
		Env:              env,
		Mode:             "task",
		ServiceAccount:   "none", // the workload gets no identity inside the project
		CPU:              fit.cpu(),
		Memory:           fit.memory(),
		MaxExecutionTime: seconds,
		RetryLimit:       0, // the default of 3 bills four runs the ledger counts as one
		ArraySpec:        "0",
	}

	if p.cfg.ImageSecret != nil {
		spec.ImageSecret = *p.cfg.ImageSecret
	}

	if err := spec.invoke(task); err != nil {
		return nil, plugin.Internal(fmt.Errorf("task %q: %w", task.Name, err))
	}

	return spec, nil
}

// -------------------------------------------------------------------------
// RESOURCES
// -------------------------------------------------------------------------

// sizeFor picks the first size with at least the CPU and memory a task
// declared, comparing memory in bytes since Code Engine's G is decimal.
func sizeFor(task *job.Task) (size, error) {
	cpu, memory := defaultCPU, defaultMemory

	if task.Resources != nil {
		if task.Resources.CPU != nil && *task.Resources.CPU > 0 {
			cpu = *task.Resources.CPU
		}

		if task.Resources.Memory != nil && *task.Resources.Memory > 0 {
			memory = *task.Resources.Memory
		}
	}

	need := float64(memory) * bytesPerMiB

	for _, s := range sizes {
		if s.millicores >= cpu && s.gb*bytesPerGB >= need {
			return s, nil
		}
	}

	return size{}, fmt.Errorf("task %q asks for %d millicores and %d MiB, more than "+
		"Code Engine's largest size of 12 vCPU and 48G", task.Name, cpu, memory)
}

// cpu is the size's vCPU as the API takes it: "0.125", "1".
func (s size) cpu() string {
	return strconv.FormatFloat(float64(s.millicores)/1000, 'f', -1, 64)
}

// memory is the size's memory as the API takes it: "0.25G", "4G".
func (s size) memory() string {
	return strconv.FormatFloat(s.gb, 'f', -1, 64) + "G"
}

// executionSeconds is the task's timeout in whole seconds, rounded up so the
// run is never cut short, or the 24h maximum when it declares none.
func executionSeconds(task *job.Task) (int, error) {
	timeout := maxDuration

	if task.Timeout != nil {
		declared, err := task.Timeout.Std()
		if err != nil {
			return 0, fmt.Errorf("task %q timeout: %w", task.Name, err)
		}

		timeout = declared
	}

	if timeout > maxDuration {
		return 0, fmt.Errorf("task %q timeout %s is past Code Engine's %s", task.Name, timeout, maxDuration)
	}

	return max(int(math.Ceil(timeout.Seconds())), 1), nil
}

// -------------------------------------------------------------------------
// THE DRIVER CONFIG
// -------------------------------------------------------------------------

// environment renders a task's environment as escaped literal variables: its
// env block plus the VAGABOND_META_* variables. One that does not evaluate is
// an internal failure, never a job run with no environment.
func environment(task *job.Task) ([]envVar, error) {
	attrs, diags := task.Environment()
	if diags.HasErrors() {
		return nil, plugin.Internal(fmt.Errorf("task %q env: %s", task.Name, diags.Error()))
	}

	env := make([]envVar, 0, len(attrs))
	for name, value := range attrs {
		env = append(env, envVar{Type: "literal", Name: name, Value: escape(value)})
	}

	// Sorted, so two submissions of the same task send identical specs
	slices.SortFunc(env, func(a, b envVar) int { return strings.Compare(a.Name, b.Name) })

	return env, nil
}

// -------------------------------------------------------------------------
// THE COMMAND LINE
// -------------------------------------------------------------------------

// invoke sets the run's command and arguments from the task's, each escaped,
// and each holding a line break carried in the environment instead.
func (s *jobRunSpec) invoke(task *job.Task) error {
	if command, ok := task.ConfigString("command"); ok {
		sent, err := s.carry(carriedCommand, command)
		if err != nil {
			return err
		}

		s.Commands = []string{sent}
	}

	if args, ok := task.ConfigStrings("args"); ok {
		s.Arguments = make([]string, len(args))

		for i, arg := range args {
			sent, err := s.carry(carriedArgPrefix+strconv.Itoa(i), arg)
			if err != nil {
				return err
			}

			s.Arguments[i] = sent
		}
	}

	return nil
}

// carry returns what to send for one command or argument. Code Engine refuses
// a line break in either, so a value holding one moves into the environment
// under name, and $(name), which Kubernetes expands at start, goes in its place.
func (s *jobRunSpec) carry(name, value string) (string, error) {
	if !strings.ContainsAny(value, "\r\n") {
		return escape(value), nil
	}

	// The task's own value would be overwritten, or would overwrite the carried one
	if slices.ContainsFunc(s.Env, func(v envVar) bool { return v.Name == name }) {
		return "", fmt.Errorf("env sets %s, which carries a multi-line command or argument", name)
	}

	s.Env = append(s.Env, envVar{Type: "literal", Name: name, Value: escape(value)})

	return "$(" + name + ")", nil
}

// escape doubles every $. Kubernetes expands $(NAME) and turns $$ into $ in a
// container's command, arguments and env values, so an escaped value reaches
// the container as written.
func escape(value string) string {
	return strings.ReplaceAll(value, "$", "$$")
}
