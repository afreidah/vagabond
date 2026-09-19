# Vagabond daemon configuration.
#
# This is the operator's file: which backends exist, what to call them, and what
# is believed to be left of each one's free tier. Job files say what to run;
# this says what is available to run it on.
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

  # What you believe is left of this month's free tier.
  #
  # A stand-in. Vagabond will keep its own ledger of what it has spent, and this
  # block goes away when it does. Until then a provider whose quota is unknown
  # is one admission refuses, so state it.
  quota {
    free_percent = 80
  }
}

provider "gcp-cloud-run" {
  type = "fake-container"

  meta {
    region = "us-central1"
  }

  # Below the fifty percent the example job's affinity prefers, so the two
  # container providers score differently and the plan has something to say.
  quota {
    free_percent = 45
  }
}

provider "aws-lambda" {
  type = "fake-function"

  quota {
    free_percent = 90
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
