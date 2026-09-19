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

provider "worker-primary" {
  type    = "fake-worker"
  enabled = false

  quota {
    exhausted = true
  }
}
