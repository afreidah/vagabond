# Vagabond daemon configuration.
#
# This is the operator's file: which backends exist, what to call them, and what
# is believed to be left of each one's free tier. Job files say what to run;
# this says what is available to run it on.
#
# Nothing here carries a credential. A provider that needs one names the secret
# and something else fetches it, because an API key in a file on disk is the
# line item in every postmortem.

# The name is what a job's provider list refers to. The type is which plugin
# implements it, and the two are separate so that one deployment can register
# the same plugin twice, against two accounts or two regions, and a job can tell
# them apart.
provider "container-primary" {
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

provider "function-primary" {
  type = "fake-function"

  quota {
    free_percent = 45
  }
}

# A provider you are not using right now.
#
# Disabled is not the same as deleted: it stays in the registry and a plan will
# tell you it was skipped because you turned it off, rather than leaving you to
# wonder why it was never considered.
provider "worker-primary" {
  type    = "fake-worker"
  enabled = false
}
