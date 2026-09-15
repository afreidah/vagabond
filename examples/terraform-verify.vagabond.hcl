# -------------------------------------------------------------------------------
# Terraform Verify - stateless CI validation
#
# Project: Vagabond / Author: Alex Freidah
#
# Runs Terraform validation using any compatible OCI job provider with available
# free-tier capacity. Admission determines which providers can currently satisfy
# the task; the scheduler chooses among those admitted candidates.
#
# If no eligible provider has capacity, Vagabond rejects the job. The caller
# decides whether to fail, retry later, or fall back to another execution path.
# -------------------------------------------------------------------------------

job "terraform-verify" {
  type = "batch"

  # ---------------------------------------------------------------------------
  # Metadata
  # ---------------------------------------------------------------------------

  meta {
    project    = "munchbox"
    repository = "afreidah/munchbox"
    purpose    = "ci"
  }

  # ---------------------------------------------------------------------------
  # Parameterized Inputs
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
  # Task: verify
  #
  # The driver declares the execution contract. `oci-job` means Vagabond needs
  # a provider capable of running an arbitrary OCI image to completion; the
  # scheduler does not need to understand how that provider implements it.
  # ---------------------------------------------------------------------------

  task "verify" {
    driver = "oci-job"

    # --- Container Configuration ---
    # Image distribution is intentionally outside the POC. Vagabond assumes
    # the selected provider can pull the image named by the job.
    config {
      image   = "hashicorp/terraform:latest"
      command = "sh"

      args = [
        "-lc",
        "terraform fmt -check -recursive && terraform validate"
      ]
    }

    # --- Environment ---
    env {
      CI               = "true"
      TF_IN_AUTOMATION = "true"
    }

    # -------------------------------------------------------------------------
    # Source
    #
    # The executor checks out this exact revision before task execution.
    # JOB_META_git_ref is supplied by the caller at submission time.
    # -------------------------------------------------------------------------

    source {
      type        = "git"
      repository  = "https://github.com/afreidah/munchbox.git"
      ref         = "${JOB_META_git_ref}"
      destination = "/workspace"
    }

    working_directory = "/workspace/infrastructure/terragrunt"

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
    # backend. Workload failures are returned directly and are not rerouted.
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
