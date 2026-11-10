// Package ibm dispatches container tasks to IBM Code Engine job runs.
//
// A job run's status comes from the v2 API, but its exit code and output do
// not: the API has no exit code field, only a failure reason, and no output at
// all. Both are read from the run's pod through the project's Kubernetes API,
// which takes the same IAM token, and they are read before the run is deleted,
// since deleting it removes the pod.
//
// Everything IBM-specific stops here. depguard forbids importing a cloud SDK
// anywhere else in the tree, so nothing above this package can accidentally
// come to depend on a Code Engine type.
package ibm
