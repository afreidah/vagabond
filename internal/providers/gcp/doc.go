// Package gcp dispatches container tasks to Google Cloud Run Jobs.
//
// Chosen as the first real provider on one decisive point: it returns what a
// job produced. The exit code is a structured integer on the task resource and
// the output comes from Cloud Logging, free to 50 GiB a month and readable the
// moment a run finishes. IBM Code Engine, the other candidate, reports its exit
// code as prose and discards output entirely once an instance completes.
//
// Everything Google-specific stops here. depguard forbids importing a cloud
// SDK anywhere else in the tree, so nothing above this package can accidentally
// come to depend on a Cloud Run type.
package gcp
