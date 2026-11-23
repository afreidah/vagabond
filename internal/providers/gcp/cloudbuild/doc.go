// Package cloudbuild dispatches container tasks to Google Cloud Build, each
// as a build of one step.
//
// Cloud Build's free build-minutes are counted apart from Cloud Run's free
// tier. A build's exit code is on the build itself, and its output comes from
// Cloud Logging, read through the gcp package both Google providers share.
package cloudbuild
