// Package api is the wire format of Vagabond's HTTP API, shared by the server
// and its clients.
//
// It holds data only, and nothing internal is encoded directly: the server
// converts to these types, so a change inside cannot change the API by
// accident.
package api
