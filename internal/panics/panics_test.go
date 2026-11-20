// -------------------------------------------------------------------------------
// Panic Containment Tests
//
// Author: Alex Freidah
//
// Catch hands a panic to its handler with the value and logs the stack, leaves
// a goroutine without one alone, and the interceptors answer Internal.
// -------------------------------------------------------------------------------

package panics

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// logged returns a logger writing text to the returned buffer.
func logged() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer

	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// A panic reaches the handler as an error naming what panicked and its value,
// and is logged with its stack.
func TestCatch_HandsThePanicOn(t *testing.T) {
	logger, buf := logged()

	var got error

	func() {
		defer Catch(t.Context(), logger, "the work", func(err error) { got = err })

		panic("index out of range")
	}()

	if got == nil || got.Error() != "panic in the work: index out of range" {
		t.Errorf("handled %v, want the panic named", got)
	}

	if out := buf.String(); !strings.Contains(out, "recovered a panic") || !strings.Contains(out, "stack=") {
		t.Errorf("logged %q, want the panic with its stack", out)
	}
}

// Without a panic the handler is not called and nothing is logged, and a nil
// handler only logs.
func TestCatch_QuietWithoutAPanic(t *testing.T) {
	logger, buf := logged()

	func() {
		defer Catch(t.Context(), logger, "the work", func(error) { t.Error("handler called without a panic") })
	}()

	func() {
		defer Catch(t.Context(), logger, "logged only", nil)

		panic(errors.New("boom"))
	}()

	if out := buf.String(); strings.Count(out, "recovered a panic") != 1 {
		t.Errorf("logged %q, want the one panic", out)
	}
}

// A panicking unary handler answers Internal, naming the method.
func TestUnaryServerInterceptor(t *testing.T) {
	logger, _ := logged()
	info := &grpc.UnaryServerInfo{FullMethod: "/vagabond.Node/Register"}

	_, err := UnaryServerInterceptor(logger)(t.Context(), nil, info, func(context.Context, any) (any, error) {
		panic("nil map")
	})

	if status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "/vagabond.Node/Register") {
		t.Errorf("err = %v, want Internal naming the method", err)
	}

	resp, err := UnaryServerInterceptor(logger)(t.Context(), nil, info, func(context.Context, any) (any, error) {
		return "ok", nil
	})

	if resp != "ok" || err != nil {
		t.Errorf("a handler that returns = %v, %v; want its answer", resp, err)
	}
}

// stream is a server stream with only a context.
type stream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the stream's context.
func (s stream) Context() context.Context {
	return s.ctx
}

// A panicking stream handler answers Internal, naming the method.
func TestStreamServerInterceptor(t *testing.T) {
	logger, _ := logged()
	info := &grpc.StreamServerInfo{FullMethod: "/vagabond.AgentExecutions/StreamLogs"}

	err := StreamServerInterceptor(logger)(nil, stream{ctx: t.Context()}, info, func(any, grpc.ServerStream) error {
		panic("closed channel")
	})

	if status.Code(err) != codes.Internal || !strings.Contains(err.Error(), "StreamLogs") {
		t.Errorf("err = %v, want Internal naming the method", err)
	}
}
