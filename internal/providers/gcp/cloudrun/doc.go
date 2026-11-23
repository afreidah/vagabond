// Package cloudrun dispatches container tasks to Google Cloud Run Jobs.
//
// What a job produced comes from two places: the exit code is a structured
// integer on the task resource, and the output comes from Cloud Logging, read
// through the gcp package both Google providers share.
package cloudrun
