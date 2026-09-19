// -------------------------------------------------------------------------------
// Capability Fixtures
//
// Author: Alex Freidah
//
// Three snapshots covering the execution families, shaped after real providers
// so that admission is exercised against limits that actually exist rather than
// invented ones. Chunk 3 reuses them, which is why they live in the package
// rather than in a test file.
//
// Nomad keeps its plugin test helpers the same way, in plugins/base/testing.go.
// -------------------------------------------------------------------------------

package plugin

import (
	"time"

	"github.com/afreidah/vagabond/internal/job"
)

// FixtureContainer is a container-job provider, shaped after IBM Code Engine.
//
// It runs arbitrary images and advertises no duration limit, which is what
// makes it the only family that can satisfy a general CI task.
//
// Four vCPU against eight gibibytes is one of Code Engine's real tiers. It does
// not sell arbitrary sizes: a plugin rounds a request up to the nearest
// combination its platform offers, and bills for the combination rather than
// for what was asked.
func FixtureContainer(observedAt time.Time) Capabilities {
	return Capabilities{
		Drivers:         []job.DriverName{job.DriverContainer},
		Architectures:   []job.Arch{job.ArchAMD64, job.ArchARM64},
		MaxResources:    Resources{CPU: 4000, Memory: 8192},
		InternetEgress:  true,
		ArbitraryImages: true,
		ObservedAt:      observedAt,
	}
}

// FixtureFunction is a function provider, shaped after AWS Lambda.
//
// The 15 minute limit is the important part: it is the real bound that makes a
// job with timeout = "15m" sit exactly on the edge, and rejecting anything past
// it is a decision admission must reach without calling AWS.
//
// ArbitraryImages is false. Lambda accepts container images, but only ones
// implementing its runtime contract, which is not the same claim.
//
// The CPU ceiling is derived rather than offered. Lambda gives no way to choose
// CPU: it scales with the memory tier, and roughly six vCPU is what the largest
// one implies. Advertising that beats leaving it zero, which this model reads
// as no limit at all.
func FixtureFunction(observedAt time.Time) Capabilities {
	return Capabilities{
		Drivers:         []job.DriverName{job.DriverFunction},
		Architectures:   []job.Arch{job.ArchAMD64, job.ArchARM64},
		MaxResources:    Resources{CPU: 6000, Memory: 10240},
		MaxDuration:     15 * time.Minute,
		InternetEgress:  true,
		ArbitraryImages: false,
		ObservedAt:      observedAt,
	}
}

// FixtureWorker is an edge provider, shaped after Cloudflare Workers.
//
// It runs no images at all and offers no architecture, because a Wasm sandbox
// has no process to run a binary in. Its limits are small enough that most CI
// work is rejected on resources even before the driver is considered.
func FixtureWorker(observedAt time.Time) Capabilities {
	return Capabilities{
		Drivers:         []job.DriverName{job.DriverWorker},
		Architectures:   nil,
		MaxResources:    Resources{Memory: 128},
		MaxDuration:     30 * time.Second,
		InternetEgress:  true,
		ArbitraryImages: false,
		ObservedAt:      observedAt,
	}
}
