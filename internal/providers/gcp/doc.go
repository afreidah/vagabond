// Package gcp dispatches container tasks to Google Cloud Run Jobs.
//
// What a job produced comes from two places: the exit code is a structured
// integer on the task resource, and the output comes from Cloud Logging, free
// to 50 GiB a month and readable the moment a run finishes.
//
// Everything Google-specific stops here. depguard forbids importing a cloud
// SDK anywhere else in the tree, so nothing above this package can accidentally
// come to depend on a Cloud Run type.
package gcp
