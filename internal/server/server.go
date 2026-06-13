// Package server provides the HTTP server exposing /metrics, /debug/state,
// and /healthz for observability tooling.
package server

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"docktunnel/internal/diagnostics"
)

// Server wraps an *http.Server with the standard DockTunnel routes registered.
type Server struct {
	srv *http.Server
}

// New builds a Server with /metrics, /debug/state, and /healthz registered.
// addr is host:port (e.g., "127.0.0.1:9100"). The snapshot function is called
// on every /debug/state request and must be goroutine-safe.
func New(addr string, snapshot func() diagnostics.DebugStateResponse) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/debug/state", diagnostics.Handler(snapshot))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return &Server{
		srv: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
}

// Start begins serving in a goroutine. It blocks until ctx is cancelled
// (graceful shutdown) or the server fails to bind. The returned error is
// non-nil only on bind failure.
func (s *Server) Start(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
