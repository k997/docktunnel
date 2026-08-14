// Package server provides the HTTP server exposing /metrics, /debug/state,
// and /healthz for observability tooling.
package server

import (
	"context"
	"crypto/subtle"
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
// on every /debug/state request and must be goroutine-safe. debugToken, when
// non-empty, gates /debug/state behind a bearer-token check.
func New(addr string, debugToken string, snapshot func() diagnostics.DebugStateResponse) *Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/debug/state", debugStateHandler(debugToken, snapshot))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return &Server{
		srv: &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}
}

// debugStateHandler wraps the snapshot handler with GET-only enforcement,
// optional bearer-token auth, and a hard timeout so a slow snapshot() can't
// hold a connection (and an FD) indefinitely. The 5s budget is well above the
// expected p99 of reading state under the controller mutex but small enough
// that a wedged lock surfaces as a 503 rather than resource exhaustion.
func debugStateHandler(token string, snapshot func() diagnostics.DebugStateResponse) http.Handler {
	h := http.TimeoutHandler(
		diagnostics.Handler(snapshot),
		5*time.Second,
		`{"error":"snapshot timeout"}`,
	)
	if token == "" {
		// Still require GET — POST/PUT shouldn't be accepted on a read endpoint.
		return getOnly(h)
	}
	return getOnly(bearerAuth(token, h))
}

// bearerAuth rejects requests whose Authorization header doesn't match
// "Bearer <token>". Uses constant-time comparison to avoid leaking the token
// via timing.
func bearerAuth(token string, next http.Handler) http.HandlerFunc {
	expected := "Bearer " + token
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="docktunnel"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	}
}

// getOnly rejects any method other than GET/HEAD with 405.
func getOnly(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
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
