package pluginruntime

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func unaryAuthInterceptor(expectedToken string) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if !authorized(ctx, expectedToken) {
			return nil, status.Error(codes.Unauthenticated, "invalid plugin runtime token")
		}
		return handler(ctx, req)
	}
}

func streamAuthInterceptor(expectedToken string) grpc.StreamServerInterceptor {
	return func(
		srv any,
		stream grpc.ServerStream,
		_ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if !authorized(stream.Context(), expectedToken) {
			return status.Error(codes.Unauthenticated, "invalid plugin runtime token")
		}
		return handler(srv, stream)
	}
}

func authorized(ctx context.Context, expectedToken string) bool {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 {
		return false
	}
	header := strings.TrimSpace(values[0])
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if provided == "" || len(provided) != len(expectedToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expectedToken)) == 1
}
