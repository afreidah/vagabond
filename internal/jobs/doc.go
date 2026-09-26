// Package jobs holds registered jobs: named, versioned definitions launched
// later by name, and the loading every caller shares.
//
// Source is stored as written and parsed again at each dispatch, because
// metadata is substituted when a job is parsed. Loading lives here so the CLI
// and the server refuse the same jobs the same way.
package jobs
