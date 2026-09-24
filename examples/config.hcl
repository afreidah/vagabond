# Vagabond daemon configuration.
#
# This is the operator's file: which backends exist, what to call them, and how
# much of each one's quota Vagabond may spend. Job files say what to run; this
# says what is available to run it on.
#
# Every provider here is a fake, which is what makes this runnable with nothing
# installed and no cloud account anywhere. The names are the real ones because
# the name is the routing identifier a job refers to, and the example job routes
# to two of them; the type is what says nothing here will actually run.
#
# Nothing here carries a credential. A provider that needs one names the secret
# and something else fetches it, because an API key in a file on disk is the
# line item in every postmortem.
#
#   vagabond job plan -config examples/config.hcl \
#     -meta version=1.2.3 examples/go-test.vagabond.hcl

# Where the usage ledger persists. Without it the ledger is kept in memory and
# starts empty every run, which is fine for trying things out and nothing else.
#
# store {
#   dsn = "postgres://vagabond@localhost:5432/vagabond"
# }

# The name is what a job's provider list refers to. The type is which plugin
# implements it, and the two are separate so that one deployment can register
# the same plugin twice, against two accounts or two regions, and a job can tell
# them apart.
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

# A provider you are not using right now.
#
# Disabled is not the same as deleted: it stays in the registry and a plan will
# tell you it was skipped because you turned it off, rather than leaving you to
# wonder why it was never considered.
provider "cloudflare-workers" {
  type    = "fake-worker"
  enabled = false
}
