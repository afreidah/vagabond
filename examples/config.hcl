# -------------------------------------------------------------------------------
# Daemon Configuration - the operator's file
#
# Author: Alex Freidah
#
# Which backends exist, what to call them, and how much of each one's quota
# Vagabond may spend. Job files say what to run; this says what is available to
# run it on.
#
# Every provider here is a fake, so this runs with nothing installed and no
# cloud account. The names are real because a job routes by name, and the
# example job routes to two of them; the type keeps anything from running.
#
# Nothing here carries a credential. A provider that needs one has a
# credentials block naming where its secret comes from, never the secret.
#
#   vagabond server -dev -config examples/config.hcl &
#   vagabond job plan -meta version=1.2.3 examples/go-test.vagabond.hcl
# -------------------------------------------------------------------------------

# Where jobs, executions and the usage ledger persist. The server requires a
# store unless it runs with -dev, which keeps everything in memory, empty at
# every start: fine for trying things out and nothing else.
#
# store {
#   dsn = "postgres://vagabond@localhost:5432/vagabond"
# }

# The name is what a job's provider list refers to and the type is the plugin
# that implements it, so one plugin can be registered twice against two
# accounts or regions.
provider "ibm-code-engine" {
  type = "fake-container"

  # Your own labels. They reach a job as provider.meta.* attributes, which a
  # constraint or affinity can match on. Vagabond never interprets them, so
  # anything you want to route on belongs here.
  meta {
    region = "us-south"
    tier   = "lite"
  }

  # What Vagabond may spend here, in the units the provider meters. Pools are
  # additive: a task must fit every pool it charges. A provider with no pools
  # enforces nothing. These follow Code Engine's free tier.
  pool "requests" {
    meter  = "executions"
    limit  = 100000
    period = "monthly"
  }

  pool "cpu" {
    meter  = "cpu_seconds"
    limit  = 100000
    period = "monthly"
  }

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 200000
    period = "monthly"
  }
}

provider "gcp-cloud-run" {
  type = "fake-container"

  meta {
    region = "us-central1"
  }

  pool "cpu" {
    meter  = "cpu_seconds"
    limit  = 180000
    period = "monthly"
  }

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 360000
    period = "monthly"
  }
}

provider "aws-lambda" {
  type = "fake-function"

  pool "requests" {
    meter  = "executions"
    limit  = 1000000
    period = "monthly"
  }

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 400000
    period = "monthly"
  }
}

# A provider you are not using right now. Disabled is not deleted: it stays in
# the registry, and a plan reports it as skipped because it is turned off.
provider "cloudflare-workers" {
  type    = "fake-worker"
  enabled = false
}
