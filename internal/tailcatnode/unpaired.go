package tailcatnode

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

// ServeUnpaired 在还没有配对身份时占住隧道端口。
// Nexus 拨进来会看到 503，而不是一条没有 device_id 的假握手。
func ServeUnpaired(ctx context.Context, ln net.Listener) error {
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "agentdock is not paired", http.StatusServiceUnavailable)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return serveListener(ctx, server, ln)
}

func serveListener(ctx context.Context, server *http.Server, ln net.Listener) error {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	err := server.Serve(ln)
	close(done)
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
