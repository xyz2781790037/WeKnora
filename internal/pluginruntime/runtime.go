package pluginruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
)

// Run starts the authenticated control plane and allowlisted egress proxy.
func Run(ctx context.Context, config Config) error {
	events := newEventBus()
	manager, err := NewManager(ctx, config, events)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", config.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen for plugin runtime gRPC: %w", err)
	}
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(unaryAuthInterceptor(config.AuthToken)),
		grpc.StreamInterceptor(streamAuthInterceptor(config.AuthToken)),
		grpc.MaxRecvMsgSize(maxRuntimeMessageSize),
		grpc.MaxSendMsgSize(maxRuntimeMessageSize),
	)
	NewGRPCServer(manager, events).Register(grpcServer)
	proxy := newEgressProxy(config.ProxyListenAddr, manager, events)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- grpcServer.Serve(listener) }()
	go func() { errorsCh <- proxy.Serve(runCtx) }()

	select {
	case <-ctx.Done():
		cancel()
		gracefulStop(grpcServer, config.ShutdownTimeout)
		return nil
	case runErr := <-errorsCh:
		cancel()
		gracefulStop(grpcServer, config.ShutdownTimeout)
		if runErr == nil || errors.Is(runErr, grpc.ErrServerStopped) {
			return nil
		}
		return runErr
	}
}

func gracefulStop(server *grpc.Server, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		server.Stop()
	}
}
