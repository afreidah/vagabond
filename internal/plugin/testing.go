// -------------------------------------------------------------------------------
// Capability Fixtures
//
// Author: Alex Freidah
//
// Three snapshots covering the execution families, shaped after real providers
// so that admission is exercised against limits that actually exist rather than
// invented ones. The fake providers are built from them, which is why they live
// in the package rather than in a test file.
// -------------------------------------------------------------------------------

package plugin

import (
	"time"

	"github.com/afreidah/vagabond/internal/job"
)

// FixtureContainer is a container-job provider, shaped after IBM Code Engine:
// arbitrary images, no duration limit, and its real 4 vCPU / 8 GiB tier.
func FixtureContainer(observedAt time.Time) Capabilities {
	return Capabilities{
		Drivers:         []job.DriverName{job.DriverContainer},
		Architectures:   []job.Arch{job.ArchAMD64, job.ArchARM64},
		MaxResources:    Limits{CPU: new(4000), Memory: new(8192)},
		InternetEgress:  true,
		ArbitraryImages: true,
		ObservedAt:      observedAt,
	}
}

// FixtureFunction is a function provider, shaped after AWS Lambda.
//
// The 15 minute limit puts a job with timeout = "15m" exactly on the edge.
// ArbitraryImages is false because Lambda only runs images implementing its
// runtime contract. The CPU ceiling is what the largest memory tier implies,
// since nil would read as no limit.
func FixtureFunction(observedAt time.Time) Capabilities {
	return Capabilities{
		Drivers:         []job.DriverName{job.DriverFunction},
		Architectures:   []job.Arch{job.ArchAMD64, job.ArchARM64},
		MaxResources:    Limits{CPU: new(6000), Memory: new(10240)},
		MaxDuration:     15 * time.Minute,
		InternetEgress:  true,
		ArbitraryImages: false,
		ObservedAt:      observedAt,
	}
}

// FixtureWorker is an edge provider, shaped after Cloudflare Workers. It offers
// no architecture, because a Wasm sandbox has no process to run a binary in.
func FixtureWorker(observedAt time.Time) Capabilities {
	return Capabilities{
		Drivers:         []job.DriverName{job.DriverWorker},
		Architectures:   nil,
		MaxResources:    Limits{Memory: new(128)},
		MaxDuration:     30 * time.Second,
		InternetEgress:  true,
		ArbitraryImages: false,
		ObservedAt:      observedAt,
	}
}
