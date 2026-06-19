package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"docktunnel/internal/diagnostics"
)

func TestNew_RegistersRoutes(t *testing.T) {
	snapshot := func() diagnostics.DebugStateResponse {
		return diagnostics.DebugStateResponse{Source: "test"}
	}
	srv := New("127.0.0.1:0", "", snapshot)

	// /healthz
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("/healthz body = %q, want ok", rec.Body.String())
	}

	// /debug/state
	req = httptest.NewRequest(http.MethodGet, "/debug/state", nil)
	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("/debug/state status = %d, want 200", rec.Code)
	}
	var resp diagnostics.DebugStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Errorf("/debug/state invalid JSON: %v", err)
	}

	// /metrics
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("/metrics status = %d, want 200", rec.Code)
	}
}

func TestStart_ReturnsOnContextCancel(t *testing.T) {
	// Find a free port by opening a listener and closing it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	snapshot := func() diagnostics.DebugStateResponse {
		return diagnostics.DebugStateResponse{}
	}
	srv := New(addr, "", snapshot)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- srv.Start(ctx)
	}()

	// Give server a moment to bind.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned error on ctx cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return within 2s of ctx cancel")
	}
}

func TestStart_PropagatesBindError(t *testing.T) {
	// Bind something on the port first.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	defer l.Close()
	addr := l.Addr().String()

	snapshot := func() diagnostics.DebugStateResponse {
		return diagnostics.DebugStateResponse{}
	}
	srv := New(addr, "", snapshot)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = srv.Start(ctx)
	if err == nil {
		t.Fatal("expected bind error, got nil")
	}
}

// TestNew_ServerTimeoutsConfigured verifies that the server's timeouts are
// set, so slowloris-style attacks and slow /debug/state snapshots can't pin
// FDs indefinitely. We assert non-zero values rather than exact numbers so
// the test isn't fragile to tuning.
func TestNew_ServerTimeoutsConfigured(t *testing.T) {
	srv := New("127.0.0.1:0", "", func() diagnostics.DebugStateResponse {
		return diagnostics.DebugStateResponse{}
	})
	if srv.srv.ReadHeaderTimeout == 0 {
		t.Error("ReadHeaderTimeout not set")
	}
	if srv.srv.ReadTimeout == 0 {
		t.Error("ReadTimeout not set")
	}
	if srv.srv.WriteTimeout == 0 {
		t.Error("WriteTimeout not set")
	}
	if srv.srv.IdleTimeout == 0 {
		t.Error("IdleTimeout not set")
	}
}

// TestDebugState_RequiresGet verifies POST/PUT/DELETE are rejected.
func TestDebugState_RequiresGet(t *testing.T) {
	srv := New("127.0.0.1:0", "", func() diagnostics.DebugStateResponse {
		return diagnostics.DebugStateResponse{Source: "test"}
	})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/debug/state", nil)
		rec := httptest.NewRecorder()
		srv.srv.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /debug/state status = %d, want 405", method, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("Allow header = %q, want %q", got, "GET, HEAD")
		}
	}

	// GET still works.
	req := httptest.NewRequest(http.MethodGet, "/debug/state", nil)
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /debug/state status = %d, want 200", rec.Code)
	}
}

// TestDebugState_BearerAuth verifies the token gate.
func TestDebugState_BearerAuth(t *testing.T) {
	const token = "s3cret-token"
	srv := New("127.0.0.1:0", token, func() diagnostics.DebugStateResponse {
		return diagnostics.DebugStateResponse{Source: "test"}
	})

	// No Authorization header → 401.
	req := httptest.NewRequest(http.MethodGet, "/debug/state", nil)
	rec := httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no auth: status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Error("expected WWW-Authenticate header on 401")
	}

	// Wrong token → 401.
	req = httptest.NewRequest(http.MethodGet, "/debug/state", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: status = %d, want 401", rec.Code)
	}

	// Correct token → 200.
	req = httptest.NewRequest(http.MethodGet, "/debug/state", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srv.srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("correct token: status = %d, want 200", rec.Code)
	}
}
