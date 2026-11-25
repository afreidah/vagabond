// -------------------------------------------------------------------------------
// Logging
//
// Author: Alex Freidah
//
// The logger the server and agent commands build from their -log-level and
// -log-json flags. Text is the default and JSON is for log collectors. Fields
// set on a context are added to every line logged with it, after the line's
// own.
// -------------------------------------------------------------------------------

package logging

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"slices"
	"strings"
)

// -------------------------------------------------------------------------
// OPTIONS
// -------------------------------------------------------------------------

// Options is how a command writes its log: the lowest level written, and
// whether each line is JSON rather than text.
type Options struct {
	Level slog.Level
	JSON  bool
}

// Register adds -log-level and -log-json to flags, which fill o when flags is
// parsed. The level defaults to info.
func (o *Options) Register(flags *flag.FlagSet) {
	o.Level = slog.LevelInfo

	flags.Var(levelFlag{&o.Level}, "log-level", "debug, info, warn or error")
	flags.BoolVar(&o.JSON, "log-json", false, "write each log line as JSON")
}

// levelFlag is -log-level's value. It takes only the four names its help
// gives, so a level slog would also read, such as "info+2", is refused rather
// than handed on to vagabond-firecracker.
type levelFlag struct {
	level *slog.Level
}

// String returns the level's name, or nothing for the empty value the flag
// package makes to read a default.
func (f levelFlag) String() string {
	if f.level == nil {
		return ""
	}

	return strings.ToLower(f.level.String())
}

// Set reads debug, info, warn or error, in any case.
func (f levelFlag) Set(s string) error {
	switch strings.ToLower(s) {
	case "debug", "info", "warn", "error":
		return f.level.UnmarshalText([]byte(s))
	}

	return errors.New("use debug, info, warn or error")
}

// -------------------------------------------------------------------------
// LOGGERS
// -------------------------------------------------------------------------

// New builds the logger o describes, writing to w. Every line carries the
// fields its context holds, and a duration prints as text, such as "1.5s", in
// JSON as well.
func New(w io.Writer, o Options) *slog.Logger {
	opts := &slog.HandlerOptions{Level: o.Level, ReplaceAttr: durationText}

	var h slog.Handler = slog.NewTextHandler(w, opts)
	if o.JSON {
		h = slog.NewJSONHandler(w, opts)
	}

	return slog.New(contextHandler{h})
}

// Named returns l with every line naming component, the part of Vagabond
// writing through it. A nil l gives a logger that discards everything.
func Named(l *slog.Logger, component string) *slog.Logger {
	if l == nil {
		return slog.New(slog.DiscardHandler)
	}

	return l.With("component", component)
}

// durationText writes a duration as its String form, which the JSON handler
// would otherwise write as a count of nanoseconds.
func durationText(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindDuration {
		a.Value = slog.StringValue(a.Value.Duration().String())
	}

	return a
}

// -------------------------------------------------------------------------
// CONTEXT FIELDS
// -------------------------------------------------------------------------

// fieldsKey is the context key that With keeps fields under.
type fieldsKey struct{}

// With returns ctx carrying args, key-value pairs as slog takes them, after
// any fields ctx carries already. A logger from New adds them to every line
// logged with the returned context or one derived from it.
func With(ctx context.Context, args ...any) context.Context {
	fields, _ := ctx.Value(fieldsKey{}).([]any)

	// Clipped, so two contexts derived from one never share the new fields
	return context.WithValue(ctx, fieldsKey{}, append(slices.Clip(fields), args...))
}

// contextHandler adds the fields a record's context carries before handing
// the record on.
type contextHandler struct {
	slog.Handler
}

// Handle adds the context's fields to a clone of r, leaving the caller's
// record as it was, and hands the clone on.
func (h contextHandler) Handle(ctx context.Context, r slog.Record) error { //nolint:gocritic // slog.Handler takes the record by value
	if fields, ok := ctx.Value(fieldsKey{}).([]any); ok {
		r = r.Clone()
		r.Add(fields...)
	}

	return h.Handler.Handle(ctx, r)
}

// WithAttrs returns a handler adding attrs to every line, still followed by
// the context's fields.
func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

// WithGroup returns a handler writing every later attribute under name, the
// context's fields included.
func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
