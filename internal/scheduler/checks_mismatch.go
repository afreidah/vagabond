// -------------------------------------------------------------------------------
// Mismatch Checks
//
// Author: Alex Freidah
//
// The rejections that are permanent for a pairing. A provider filtered here
// cannot run this task today or next week, and the only thing that changes the
// answer is changing the job.
//
// Ordered cheapest first within the tier: set membership, then booleans, then
// integer comparisons, then constraint evaluation, which is the only one that
// builds a map and parses operands. Nomad orders its stack on the same
// principle and says so where it wires the drivers check ahead of constraints.
// -------------------------------------------------------------------------------

package scheduler

import (
	"fmt"
	"slices"
	"strings"

	"github.com/afreidah/vagabond/internal/plugin"
)

// -------------------------------------------------------------------------
// EXECUTION CONTRACT
// -------------------------------------------------------------------------

// driverChecker removes providers that do not offer the task's driver.
type driverChecker struct{}

// Name identifies the checker in traces and test failures.
func (driverChecker) Name() string { return "driver" }

// Check rejects a provider that does not run this execution family.
func (driverChecker) Check(req *Request, in *Input) *Rejection {
	driver := req.Task.Driver
	if slices.Contains(in.Capabilities.Drivers, driver) {
		return nil
	}

	offered := make([]string, 0, len(in.Capabilities.Drivers))
	for _, d := range in.Capabilities.Drivers {
		offered = append(offered, d.String())
	}

	return reject(ReasonDriverUnsupported, fmt.Sprintf(
		"The task uses the %s driver and this provider offers %s.",
		driver, orNone(offered)))
}

// archChecker removes providers that do not offer the required architecture.
type archChecker struct{}

// Name identifies the checker in traces and test failures.
func (archChecker) Name() string { return "arch" }

// Check rejects a provider that cannot run the architecture the task named.
//
// A task that named none is admitted everywhere. Most work does not care, and
// forcing an author to state an architecture to get a placement would make the
// common job longer to say nothing.
func (archChecker) Check(req *Request, in *Input) *Rejection {
	arch, required := req.Architecture()
	if !required || slices.Contains(in.Capabilities.Architectures, arch) {
		return nil
	}

	offered := make([]string, 0, len(in.Capabilities.Architectures))
	for _, a := range in.Capabilities.Architectures {
		offered = append(offered, a.String())
	}

	return reject(ReasonArchUnsupported, fmt.Sprintf(
		"The task requires %s and this provider offers %s.", arch, orNone(offered)))
}

// imageChecker removes providers that will not run the image a task names.
type imageChecker struct{}

// Name identifies the checker in traces and test failures.
func (imageChecker) Name() string { return "image" }

// Check rejects a provider that runs containers but not anyone's container.
//
// Distinct from the driver check because the two claims differ. Lambda accepts
// container images, but only ones implementing its runtime contract, so a
// provider can offer an execution family and still refuse the image a job
// brought to it.
func (imageChecker) Check(req *Request, in *Input) *Rejection {
	if req.Image == "" || in.Capabilities.ArbitraryImages {
		return nil
	}

	return reject(ReasonImageUnsupported, fmt.Sprintf(
		"The task runs %s and this provider runs only its own images.", req.Image))
}

// -------------------------------------------------------------------------
// LIMITS
// -------------------------------------------------------------------------

// resourcesChecker removes providers smaller than the task requires.
type resourcesChecker struct{}

// Name identifies the checker in traces and test failures.
func (resourcesChecker) Name() string { return "resources" }

// Check rejects a provider whose ceiling is below what the task asked for.
//
// A limit of zero means the provider advertised none, which admits any request.
// Reading it as a real ceiling of zero would reject every task against a
// provider that simply declined to state a bound.
func (resourcesChecker) Check(req *Request, in *Input) *Rejection {
	max := in.Capabilities.MaxResources

	if cpu := req.CPU(); max.CPU > 0 && cpu > max.CPU {
		return reject(ReasonResourcesExceeded, fmt.Sprintf(
			"The task asks for %d millicores and this provider allows %d.", cpu, max.CPU))
	}

	if memory := req.Memory(); max.Memory > 0 && memory > max.Memory {
		return reject(ReasonResourcesExceeded, fmt.Sprintf(
			"The task asks for %d MiB and this provider allows %d.", memory, max.Memory))
	}

	return nil
}

// durationChecker removes providers that would kill the task before its
// timeout.
type durationChecker struct{}

// Name identifies the checker in traces and test failures.
func (durationChecker) Name() string { return "duration" }

// Check rejects a provider whose limit is below the task's declared timeout.
//
// A task killed by a provider limit under its own timeout is an admission bug
// rather than a workload failure, which is the whole reason this comparison
// happens before dispatch rather than being discovered after it.
//
// An unparseable timeout is passed over rather than rejected here. Jobspec
// validation owns that error and reports it against the line the author wrote,
// where admission could only say that some provider or other declined.
func (durationChecker) Check(req *Request, in *Input) *Rejection {
	max := in.Capabilities.MaxDuration
	if max == 0 || req.Task.Timeout == nil {
		return nil
	}

	timeout, err := req.Task.Timeout.Std()
	if err != nil || timeout <= max {
		return nil
	}

	return reject(ReasonDurationExceeded, fmt.Sprintf(
		"The task's %s timeout exceeds the %s this provider allows.",
		timeout, max))
}

// networkChecker removes providers that cannot offer the connectivity a task
// requires.
type networkChecker struct{}

// Name identifies the checker in traces and test failures.
func (networkChecker) Name() string { return "network" }

// Check rejects a provider missing connectivity the task asked for.
//
// Only an explicit true is a requirement. An absent internet leaves the choice
// to the provider and an explicit false denies egress, neither of which any
// provider can fail to satisfy.
func (networkChecker) Check(req *Request, in *Input) *Rejection {
	if req.NeedsInternet() && !in.Capabilities.InternetEgress {
		return reject(ReasonNetworkUnsupported,
			"The task requires internet egress and this provider offers none.")
	}

	if req.NeedsPrivateNetwork() && !in.Capabilities.PrivateNetwork {
		return reject(ReasonNetworkUnsupported,
			"The task requires private networking and this provider offers none.")
	}

	return nil
}

// -------------------------------------------------------------------------
// ATTRIBUTES
// -------------------------------------------------------------------------

// attributeChecker removes providers when the job names an attribute Vagabond
// does not publish.
type attributeChecker struct{}

// Name identifies the checker in traces and test failures.
func (attributeChecker) Name() string { return "attribute" }

// Check rejects when a constraint or affinity names an unpublished attribute.
//
// The same verdict lands on every provider, which is the point: a typo is a
// property of the job file rather than of any backend, and a plan whose every
// row says so reads as a job file to fix. Reporting it as an unmet constraint
// instead is how a misspelling comes to look like an outage.
//
// Ahead of the constraint check because an attribute that does not exist cannot
// meaningfully fail to match.
func (attributeChecker) Check(req *Request, _ *Input) *Rejection {
	unknown := UnknownAttribute(req.Constraints(), req.Affinities())
	if unknown == "" {
		return nil
	}

	return reject(ReasonAttributeUnknown, fmt.Sprintf(
		"Nothing publishes %q. Provider attributes are %s, and an operator's own "+
			"tags live under %s.",
		unknown, strings.Join(plugin.KnownAttributes(), ", "), plugin.MetaPrefix))
}

// constraintChecker removes providers that fail the job's hard requirements.
type constraintChecker struct{}

// Name identifies the checker in traces and test failures.
func (constraintChecker) Name() string { return "constraint" }

// Check rejects a provider that does not satisfy every constraint.
func (constraintChecker) Check(req *Request, in *Input) *Rejection {
	constraints := req.Constraints()
	if len(constraints) == 0 {
		return nil
	}

	attrs := in.Attributes(req.Execution)

	unmet := UnmatchedConstraint(attrs, constraints)
	if unmet == nil {
		return nil
	}

	return reject(ReasonConstraintUnmet, fmt.Sprintf(
		"The job requires %s %s %q and this provider publishes %s.",
		unmet.Attribute, unmet.Operator, unmet.Value,
		publishedValue(attrs, unmet.Attribute)))
}

// -------------------------------------------------------------------------
// RENDERING
// -------------------------------------------------------------------------

// orNone renders a list of offerings, naming the empty case rather than
// printing a blank where a reader expects a value.
func orNone(values []string) string {
	if len(values) == 0 {
		return "none"
	}

	return strings.Join(values, ", ")
}

// publishedValue renders what a provider says about an attribute, naming the
// absent case so that "unset" and "empty" do not read alike.
func publishedValue(attrs map[string]string, name string) string {
	value, ok := attrs[name]
	if !ok {
		return "nothing for it"
	}

	return fmt.Sprintf("%q", value)
}
