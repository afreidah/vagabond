// Package logging builds the one logger the server and agent commands write
// through, so their lines share a format, name the component that wrote them,
// and carry the IDs of the work they are about without each call repeating
// them.
//
// A field set on a context with With reaches every line logged with that
// context, including lines written several packages below the code that set
// it: the server sets a dispatch's ID where the dispatch starts, and the
// dispatcher's own lines about that dispatch carry it.
package logging
