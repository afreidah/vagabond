# -------------------------------------------------------------------------------
# Go Test - stateless CI test run
#
# Project: Vagabond / Author: Alex Freidah
#
# Runs a repository's test suite on any container provider with free-tier
# capacity available. Admission determines which providers can satisfy the task;
# the scheduler chooses among those admitted candidates.
#
# Nothing about this job is special to Vagabond. It names an image, a command,
# and what the task needs to run; Vagabond neither knows nor cares what the
# command does.
#
# If no eligible provider has capacity, Vagabond rejects the job. The caller
# decides whether to fail, retry later, or fall back to another execution path.
# -------------------------------------------------------------------------------

job "go-test" {
  type = "batch"

  # ---------------------------------------------------------------------------
  # Metadata
  #
  # Arbitrary key/value pairs carried with the job. Vagabond stores and reports
  # them and is otherwise indifferent to what they mean.
  # ---------------------------------------------------------------------------

  meta {
    project = "example"
    purpose = "ci"
  }

  # ---------------------------------------------------------------------------
  # Parameterized Inputs
  #
  # Metadata the caller must supply at submission. A job referencing a key that
  # was not supplied is rejected before any provider is contacted.
  # ---------------------------------------------------------------------------

  parameterized {
    meta_required = ["version"]
  }

  # ---------------------------------------------------------------------------
  # Routing
  #
  # Provider order is a preference, not a hard failover chain. Admission first
  # removes providers that cannot satisfy the task driver, resource constraints,
  # cost policy, or current quota. The scheduler scores the remaining candidates.
  # ---------------------------------------------------------------------------

  routing {
    strategy = "free-first"

    providers = [
      "ibm-code-engine",
      "gcp-cloud-run",
    ]

    # --- This workload must never intentionally consume paid compute. ---
    max_cost_usd = 0

    # A provider may offer several architectures, so the attribute holds a set
    # and membership is the question. Equality would ask whether amd64 is the
    # only one it offers.
    constraint {
      attribute = "provider.architecture"
      operator  = "set_contains"
      value     = "amd64"
    }

    # Prefer providers with plenty of monthly quota remaining so scarce
    # capacity remains available for workloads with fewer eligible backends.
    affinity {
      attribute = "provider.free_quota_percent"
      operator  = ">"
      value     = "50"
      weight    = 75
    }
  }

  # ---------------------------------------------------------------------------
  # Task: test
  #
  # The driver declares the execution contract. `container` means Vagabond needs
  # a provider capable of running an arbitrary container image to completion;
  # the scheduler does not need to understand how that provider implements it.
  # ---------------------------------------------------------------------------

  task "test" {
    driver = "container"

    # --- Container Configuration ---
    # Image distribution is intentionally outside the POC. Vagabond assumes
    # the selected provider can pull the image named by the job.
    config {
      image   = "golang:1.27"
      command = "go"

      args = [
        "test",
        "./...",
      ]
    }

    # --- Environment ---
    env {
      CI          = "true"
      CGO_ENABLED = "0"
    }

    # -------------------------------------------------------------------------
    # Source
    #
    # The executor checks out this exact revision before the task runs.
    #
    # meta.version is supplied by the caller at submission time and substituted
    # before dispatch, so the provider receives a literal revision. Task
    # metadata is also injected into the running container as JOB_META_version,
    # for commands that want to read it themselves.
    # -------------------------------------------------------------------------

    source {
      type        = "git"
      repository  = "https://git.example.com/example/service.git"
      ref         = "${meta.version}"
      destination = "/workspace"
    }

    working_directory = "/workspace"

    # -------------------------------------------------------------------------
    # Resources
    #
    # Workload requirements rather than provider-specific settings. Each
    # provider plugin translates these to the closest supported configuration.
    # -------------------------------------------------------------------------

    resources {
      # Millicores; 1000 is one vCPU. Not MHz, because no cloud sells clock
      # speed. A plugin rounds up to the nearest size its platform offers.
      cpu    = 1000
      memory = 2048
    }

    timeout = "15m"

    # -------------------------------------------------------------------------
    # Network
    # -------------------------------------------------------------------------

    network {
      internet = true
      private  = false
    }

    # -------------------------------------------------------------------------
    # Execution Requirements
    # -------------------------------------------------------------------------

    execution {
      architecture = "amd64"
      privileged   = false
    }

    # -------------------------------------------------------------------------
    # Retry Policy
    #
    # Provider/infrastructure failures may be rerouted to another admitted
    # backend. Workload failures are returned directly and are not rerouted: a
    # failing test suite is an answer, not an outage.
    # -------------------------------------------------------------------------

    retry {
      attempts = 2
      reroute  = true

      backoff {
        initial = "5s"
        max     = "30s"
      }
    }
  }
}
