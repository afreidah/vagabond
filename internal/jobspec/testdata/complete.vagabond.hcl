# -------------------------------------------------------------------------------
# Complete Job - parser test fixture
#
# Project: Vagabond / Author: Alex Freidah
#
# Exercises every block and attribute the specification defines, so that the
# parser is tested against a job using all of them at once.
#
# This is a fixture, not an example. It lives here rather than in examples/ so
# that the test suite does not break when an illustrative file is edited, and so
# that a field can be added here to cover it without changing what a reader is
# shown. A separate test checks that everything under examples/ parses.
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
    meta_required = ["git_ref"]
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

    constraint {
      attribute = "provider.architecture"
      operator  = "="
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
    # meta.git_ref is supplied by the caller at submission time and substituted
    # before dispatch, so the provider receives a literal revision. Task
    # metadata is also injected into the running container as JOB_META_git_ref,
    # for commands that want to read it themselves.
    # -------------------------------------------------------------------------

    source {
      type        = "git"
      repository  = "https://git.example.com/example/service.git"
      ref         = "${meta.git_ref}"
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
