// Package gcp holds what Vagabond's Google providers share: the signed client,
// the config block both take, and Cloud Logging, where both read a container's
// output. The providers are its subpackages, cloudrun and cloudbuild, and
// gcptest is the fake Google their tests run against.
//
// Everything Google-specific stops in these packages. depguard forbids
// importing a cloud SDK anywhere else in the tree, so nothing above them can
// accidentally come to depend on a Google type.
package gcp
