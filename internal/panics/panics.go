// -------------------------------------------------------------------------------
// Panic Containment
//
// Author: Alex Freidah
//
// A panic ends the unit of work it happened in, never the process: deferred at
// the top of a goroutine or a call, Catch turns the panic into an error for
// the caller to act on, after logging it with its stack.
// -------------------------------------------------------------------------------

package panics

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Catch, deferred in a goroutine or call, turns a panic there into an error
// for handle, after logging it at ERROR with its stack. Without a panic it
// returns at once, and handle may be nil when logging is all there is to do.
func Catch(ctx context.Context, logger *slog.Logger, what string, handle func(error)) {
	r := recover()
	if r == nil {
		return
	}

	err := fmt.Errorf("panic in %s: %v", what, r)
	logger.ErrorContext(ctx, "recovered a panic", "in", what, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))

	if handle != nil {
		handle(err)
	}
}

// UnaryServerInterceptor answers a unary handler's panic with codes.Internal,
// naming the method but not the panic, rather than ending the process.
func UnaryServerInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer Catch(ctx, logger, info.FullMethod, func(error) { err = internal(info.FullMethod) })

		return handler(ctx, req)
	}
}

// StreamServerInterceptor answers a streaming handler's panic the way
// UnaryServerInterceptor answers a unary one's.
func StreamServerInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer Catch(ss.Context(), logger, info.FullMethod, func(error) { err = internal(info.FullMethod) })

		return handler(srv, ss)
	}
}

// internal is the status a recovered panic in method answers with.
func internal(method string) error {
	return status.Errorf(codes.Internal, "internal error in %s", method)
}
