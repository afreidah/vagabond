# A deployment registering one provider per execution family.
#
# Every provider here is a fake, which is the point: the whole path from a
# configuration file to the inputs admission reads has to work with no cloud
# account configured anywhere.

provider "container-primary" {
  type = "fake-container"

  meta {
    region = "us-south"
    tier   = "lite"
  }
}

provider "function-primary" {
  type = "fake-function"

  pool "compute" {
    meter  = "gb_seconds"
    limit  = 400000
    period = "monthly"
  }
}

provider "worker-primary" {
  type    = "fake-worker"
  enabled = false
}
