// -------------------------------------------------------------------------------
// Semantic Validation
//
// Author: Alex Freidah
//
// Decoding proves a file has the right shape; these rules ask whether the job
// means anything, such as a backoff whose maximum is below its initial or a
// timeout of zero. A question needing a capability snapshot belongs to
// admission instead. Diagnostics carry no source range, because the rules run
// against the decoded specification, so a message names the job and task.
// -------------------------------------------------------------------------------

package jobspec

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/afreidah/vagabond/internal/job"
	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/ptr"
)

// -------------------------------------------------------------------------
// ENTRY POINT
// -------------------------------------------------------------------------

// Validate reports every rule a file breaks, collecting rather than stopping at
// the first so an author can fix a file in one pass.
func Validate(file *job.File) hcl.Diagnostics {
	if file == nil {
		return nil
	}

	var diags hcl.Diagnostics

	if len(file.Jobs) == 0 {
		return append(diags, simple("Empty specification",
			"A job file must declare a job."))
	}

	// File holds a slice so a second job is reported here, not discarded
	if len(file.Jobs) > 1 {
		diags = append(diags, simple("Too many jobs",
			fmt.Sprintf("A job file declares one job, and this one declares %d: %s.",
				len(file.Jobs), strings.Join(jobNames(file.Jobs), ", "))))
	}

	for i := range file.Jobs {
		diags = append(diags, validateJob(&file.Jobs[i])...)
	}

	return diags
}

// -------------------------------------------------------------------------
// JOBS
// -------------------------------------------------------------------------

// validateJob checks one job and everything under it.
func validateJob(j *job.Job) hcl.Diagnostics {
	var diags hcl.Diagnostics

	if strings.TrimSpace(j.Name) == "" {
		diags = append(diags, simple("Missing job name",
			"A job block must carry a name."))
	}

	if j.Namespace != nil && strings.TrimSpace(*j.Namespace) == "" {
		diags = append(diags, simple(fmt.Sprintf("Empty namespace in %q", j.Name),
			"Remove the attribute to use the default namespace."))
	}

	diags = append(diags, validateJobType(j)...)
	diags = append(diags, validateRouting(j)...)
	diags = append(diags, validateTasks(j)...)

	return diags
}

// validateJobType enforces the closed vocabulary, which decoding cannot: gohcl
// converts by reflected kind and never calls UnmarshalText.
func validateJobType(j *job.Job) hcl.Diagnostics {
	if j.Type == nil {
		return nil
	}

	if ptr.Deref(j.Type).Valid() {
		return nil
	}

	return hcl.Diagnostics{simple(
		fmt.Sprintf("Unknown job type in %q", j.Name),
		fmt.Sprintf("Type %q is not one Vagabond implements. Valid types are %s.",
			ptr.Deref(j.Type), joinTypes()))}
}

// -------------------------------------------------------------------------
// ROUTING
// -------------------------------------------------------------------------

// validateRouting checks the provider selection policy.
func validateRouting(j *job.Job) hcl.Diagnostics {
	if j.Routing == nil {
		return nil
	}

	var diags hcl.Diagnostics

	if j.Routing.Strategy != nil && !ptr.Deref(j.Routing.Strategy).Valid() {
		diags = append(diags, simple(
			fmt.Sprintf("Unknown routing strategy in %q", j.Name),
			fmt.Sprintf("Strategy %q is not one Vagabond implements. Valid strategies are %s.",
				ptr.Deref(j.Routing.Strategy), joinStrategies())))
	}

	if j.Routing.Tiers != nil && !ptr.Deref(j.Routing.Tiers).Valid() {
		diags = append(diags, simple(
			fmt.Sprintf("Unknown tier mode in %q", j.Name),
			fmt.Sprintf("Tiers %q is not a mode Vagabond implements. Valid modes are %s.",
				ptr.Deref(j.Routing.Tiers), joinTierModes())))
	}

	for i := range j.Routing.Constraints {
		constraint := &j.Routing.Constraints[i]

		diags = append(diags, validateAttribute(j.Name, "constraint", constraint.Attribute)...)
		diags = append(diags, validateOperator(j.Name, "constraint",
			constraint.Operator, constraint.Value)...)
	}

	for i := range j.Routing.Affinities {
		affinity := &j.Routing.Affinities[i]

		diags = append(diags, validateAttribute(j.Name, "affinity", affinity.Attribute)...)
		diags = append(diags, validateOperator(j.Name, "affinity",
			affinity.Operator, affinity.Value)...)

		if affinity.Weight != nil && ptr.Deref(affinity.Weight) <= 0 {
			diags = append(diags, simple(
				fmt.Sprintf("Invalid affinity weight in %q", j.Name),
				fmt.Sprintf("Weight %d does not raise a provider's score. "+
					"Use a positive weight, or remove the affinity.",
					ptr.Deref(affinity.Weight))))
		}
	}

	return diags
}

// validateAttribute accepts a published attribute or anything under the
// unchecked provider.meta. and node.label. prefixes; others match nothing.
func validateAttribute(jobName, kind, attribute string) hcl.Diagnostics {
	if plugin.Matchable(attribute) {
		return nil
	}

	detail := fmt.Sprintf("Attribute %q is not one Vagabond publishes about a "+
		"provider. Known attributes are %s. Anything under %q or %q is an "+
		"operator's own and is not checked.",
		attribute, strings.Join(plugin.KnownAttributes(), ", "),
		plugin.MetaPrefix, plugin.NodeLabelPrefix)

	return hcl.Diagnostics{simple(
		fmt.Sprintf("Unknown %s attribute in %q", kind, jobName), detail)}
}

// validateOperator checks the comparison and its value: a presence operator
// takes none, and every other operator needs one.
func validateOperator(jobName, kind string, op job.Operator, value string) hcl.Diagnostics {
	if !op.Valid() {
		return hcl.Diagnostics{simple(
			fmt.Sprintf("Unknown %s operator in %q", kind, jobName),
			fmt.Sprintf("Operator %q is not one Vagabond implements. "+
				"Valid operators are %s.", op, joinOperators()))}
	}

	if op.Presence() && value != "" {
		return hcl.Diagnostics{simple(
			fmt.Sprintf("Unexpected %s value in %q", kind, jobName),
			fmt.Sprintf("Operator %q asks only whether the attribute is published, "+
				"so the value %q is not used. Remove it, or use a comparison.",
				op, value))}
	}

	if !op.Presence() && value == "" {
		return hcl.Diagnostics{simple(
			fmt.Sprintf("Missing %s value in %q", kind, jobName),
			fmt.Sprintf("Operator %q compares against a value, and none was given.", op))}
	}

	return nil
}

// -------------------------------------------------------------------------
// TASKS
// -------------------------------------------------------------------------

// validateTasks checks that a job has tasks, that their names are distinct, and
// then each of them.
func validateTasks(j *job.Job) hcl.Diagnostics {
	var diags hcl.Diagnostics

	if len(j.Tasks) == 0 {
		return append(diags, simple(
			fmt.Sprintf("No tasks in %q", j.Name),
			"A job must declare at least one task; there is nothing to run otherwise."))
	}

	seen := make(map[string]bool, len(j.Tasks))

	for i := range j.Tasks {
		task := &j.Tasks[i]

		if seen[task.Name] {
			diags = append(diags, simple(
				fmt.Sprintf("Duplicate task name in %q", j.Name),
				fmt.Sprintf("Two tasks are named %q. Task names identify a task "+
					"in results and logs, so they must differ.", task.Name)))
		}

		seen[task.Name] = true

		diags = append(diags, validateTask(j.Name, task)...)
	}

	return diags
}

// validateTask checks one task.
func validateTask(jobName string, task *job.Task) hcl.Diagnostics {
	var diags hcl.Diagnostics

	if strings.TrimSpace(task.Name) == "" {
		diags = append(diags, simple(
			fmt.Sprintf("Missing task name in %q", jobName),
			"A task block must carry a name."))
	}

	diags = append(diags, validateDriver(jobName, task)...)
	diags = append(diags, validateConfig(jobName, task)...)
	diags = append(diags, validateExecution(jobName, task)...)
	diags = append(diags, validateResources(jobName, task)...)
	diags = append(diags, validateTimeout(jobName, task)...)
	diags = append(diags, validateRetry(jobName, task)...)

	return diags
}

// validateDriver enforces the closed driver vocabulary, for the same reason
// validateJobType does.
func validateDriver(jobName string, task *job.Task) hcl.Diagnostics {
	if task.Driver.Valid() {
		return nil
	}

	return hcl.Diagnostics{simple(
		fmt.Sprintf("Unknown driver in %q task %q", jobName, task.Name),
		fmt.Sprintf("Driver %q is not an execution contract Vagabond implements. "+
			"Valid drivers are %s.", task.Driver, joinDrivers()))}
}

// validateConfig checks what the driver contract implies, that a container
// task's config names an image; anything more is the plugin's to reject.
func validateConfig(jobName string, task *job.Task) hcl.Diagnostics {
	if task.Driver != job.DriverContainer {
		return nil
	}

	missing := hcl.Diagnostics{simple(
		fmt.Sprintf("Missing image in %q task %q", jobName, task.Name),
		"A container task runs an image, so its config block must set one.")}

	if task.Config == nil || task.Config.Body == nil {
		return missing
	}

	attrs, diags := task.Config.Body.JustAttributes()
	if diags.HasErrors() {
		return diags
	}

	if _, ok := attrs["image"]; !ok {
		return missing
	}

	return nil
}

// validateExecution checks the environment a task requires.
func validateExecution(jobName string, task *job.Task) hcl.Diagnostics {
	if task.Execution == nil || task.Execution.Architecture == nil {
		return nil
	}

	arch := ptr.Deref(task.Execution.Architecture)
	if arch.Valid() {
		return nil
	}

	return hcl.Diagnostics{simple(
		fmt.Sprintf("Unknown architecture in %q task %q", jobName, task.Name),
		fmt.Sprintf("Architecture %q is not one Vagabond schedules against. "+
			"Valid architectures are %s.", arch, joinArches()))}
}

// validateResources checks that requested resources are positive. Zero is
// rejected, since omitting the value already means no preference.
func validateResources(jobName string, task *job.Task) hcl.Diagnostics {
	if task.Resources == nil {
		return nil
	}

	var diags hcl.Diagnostics

	if task.Resources.CPU != nil && ptr.Deref(task.Resources.CPU) <= 0 {
		diags = append(diags, simple(
			fmt.Sprintf("Invalid CPU request in %q task %q", jobName, task.Name),
			fmt.Sprintf("CPU is %d. Request a positive number of millicores, "+
				"where 1000 is one vCPU, or omit it to state no requirement.",
				ptr.Deref(task.Resources.CPU))))
	}

	if task.Resources.Memory != nil && ptr.Deref(task.Resources.Memory) <= 0 {
		diags = append(diags, simple(
			fmt.Sprintf("Invalid memory request in %q task %q", jobName, task.Name),
			fmt.Sprintf("Memory is %d. Request a positive number of MiB, or omit it "+
				"to state no requirement.", ptr.Deref(task.Resources.Memory))))
	}

	return diags
}

// validateTimeout checks that a declared timeout parses and is nonzero. Parsing
// happens here because gohcl leaves a Duration as the text the file carried.
func validateTimeout(jobName string, task *job.Task) hcl.Diagnostics {
	if task.Timeout == nil {
		return nil
	}

	timeout := ptr.Deref(task.Timeout)

	parsed, err := timeout.Std()
	if err != nil {
		return hcl.Diagnostics{simple(
			fmt.Sprintf("Invalid timeout in %q task %q", jobName, task.Name),
			fmt.Sprintf("Timeout %q is not a duration. Write one as 15m, 90s, or 1h30m.",
				timeout))}
	}

	if parsed == 0 {
		return hcl.Diagnostics{simple(
			fmt.Sprintf("Invalid timeout in %q task %q", jobName, task.Name),
			"A timeout of zero bounds nothing. Give the task a duration, or omit "+
				"the attribute to leave it unbounded.")}
	}

	return nil
}

// -------------------------------------------------------------------------
// RETRY
// -------------------------------------------------------------------------

// validateRetry checks the retry policy and its backoff.
func validateRetry(jobName string, task *job.Task) hcl.Diagnostics {
	if task.Retry == nil {
		return nil
	}

	var diags hcl.Diagnostics

	if task.Retry.Attempts != nil && ptr.Deref(task.Retry.Attempts) < 0 {
		diags = append(diags, simple(
			fmt.Sprintf("Invalid retry attempts in %q task %q", jobName, task.Name),
			fmt.Sprintf("Attempts is %d. Use zero to forbid retrying, or a positive "+
				"number to allow it.", ptr.Deref(task.Retry.Attempts))))
	}

	return append(diags, validateBackoff(jobName, task)...)
}

// validateBackoff checks that the delays between attempts make sense.
func validateBackoff(jobName string, task *job.Task) hcl.Diagnostics {
	backoff := task.Retry.Backoff
	if backoff == nil {
		return nil
	}

	var diags hcl.Diagnostics

	initial, initialOK := checkDuration(jobName, task, "backoff initial", backoff.Initial, &diags)
	maximum, maxOK := checkDuration(jobName, task, "backoff max", backoff.Max, &diags)

	// A maximum below the initial delay is never reached
	if initialOK && maxOK && maximum < initial {
		diags = append(diags, simple(
			fmt.Sprintf("Invalid backoff in %q task %q", jobName, task.Name),
			fmt.Sprintf("Backoff max %q is below initial %q, so the maximum is "+
				"never reached.", ptr.Deref(backoff.Max), ptr.Deref(backoff.Initial))))
	}

	return diags
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// checkDuration parses an optional duration, reporting it and returning whether
// the value is usable.
func checkDuration(
	jobName string,
	task *job.Task,
	field string,
	value *job.Duration,
	diags *hcl.Diagnostics,
) (parsed int64, ok bool) {
	if value == nil {
		return 0, false
	}

	duration, err := ptr.Deref(value).Std()
	if err != nil {
		*diags = append(*diags, simple(
			fmt.Sprintf("Invalid %s in %q task %q", field, jobName, task.Name),
			fmt.Sprintf("Value %q is not a duration. Write one as 15m, 90s, or 1h30m.",
				ptr.Deref(value))))

		return 0, false
	}

	return int64(duration), true
}

// simple builds a diagnostic with no source range.
func simple(summary, detail string) *hcl.Diagnostic {
	return &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
	}
}

// jobNames lists the jobs a file declares, for a message about there being too
// many of them.
func jobNames(jobs []job.Job) []string {
	names := make([]string, 0, len(jobs))
	for i := range jobs {
		names = append(names, fmt.Sprintf("%q", jobs[i].Name))
	}

	return names
}

// joinDrivers lists the valid drivers for a diagnostic.
func joinDrivers() string {
	names := make([]string, 0, len(job.Drivers()))
	for _, d := range job.Drivers() {
		names = append(names, d.String())
	}

	return strings.Join(names, ", ")
}

// joinTypes lists the valid job types for a diagnostic.
func joinTypes() string {
	names := make([]string, 0, len(job.Types()))
	for _, t := range job.Types() {
		names = append(names, t.String())
	}

	return strings.Join(names, ", ")
}

// joinArches lists the valid architectures for a diagnostic.
func joinArches() string {
	names := make([]string, 0, len(job.Arches()))
	for _, a := range job.Arches() {
		names = append(names, a.String())
	}

	return strings.Join(names, ", ")
}

// joinStrategies lists the valid routing strategies for a diagnostic.
func joinStrategies() string {
	names := make([]string, 0, len(job.Strategies()))
	for _, s := range job.Strategies() {
		names = append(names, s.String())
	}

	return strings.Join(names, ", ")
}

// joinTierModes lists the valid tier modes for a diagnostic.
func joinTierModes() string {
	names := make([]string, 0, len(job.TierModes()))
	for _, m := range job.TierModes() {
		names = append(names, m.String())
	}

	return strings.Join(names, ", ")
}

// joinOperators lists the valid operators for a diagnostic.
func joinOperators() string {
	names := make([]string, 0, len(job.Operators()))
	for _, o := range job.Operators() {
		names = append(names, o.String())
	}

	return strings.Join(names, ", ")
}
