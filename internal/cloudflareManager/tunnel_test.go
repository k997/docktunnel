package cloudflareManager

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"testing"
	"time"

	"docktunnel/pkg/types"
	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/dns"
	"golang.org/x/time/rate"
)

// TestNewHTTPClient_HasTimeouts verifies the dedicated HTTP client used by
// NewManager carries explicit timeouts: the SDK default http.DefaultClient has
// none, so a hung Cloudflare connection could block startup/sync forever. The
// client-level Timeout is the final backstop; transport-level timeouts bound
// connection, TLS handshake and response headers individually.
func TestNewHTTPClient_HasTimeouts(t *testing.T) {
	c := newHTTPClient()
	if c.Timeout <= 0 {
		t.Errorf("expected a positive client-level Timeout, got %v", c.Timeout)
	}

	transport, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", c.Transport)
	}
	if transport.DialContext == nil {
		t.Error("expected a DialContext with connect timeout")
	}
	if transport.TLSHandshakeTimeout <= 0 {
		t.Errorf("expected positive TLSHandshakeTimeout, got %v", transport.TLSHandshakeTimeout)
	}
	if transport.ResponseHeaderTimeout <= 0 {
		t.Errorf("expected positive ResponseHeaderTimeout, got %v", transport.ResponseHeaderTimeout)
	}
	if transport.IdleConnTimeout <= 0 {
		t.Errorf("expected positive IdleConnTimeout, got %v", transport.IdleConnTimeout)
	}
}

func TestNewManager(t *testing.T) {
	// 跳过需要实际API调用的测试
	// 这些测试需要有效的Cloudflare账户和API令牌
	t.Skip("Skipping test that requires valid Cloudflare credentials")

	// 测试创建Cloudflare管理器
	opts := ManagerOptions{
		AccountID:     "test-account-id",
		APIToken:      "test-api-token",
		TunnelID:      "",
		TunnelName:    "",
		RateLimit:     10,
		MaxRetries:    3,
		RetryDelay:    1 * time.Second,
		MaxRetryDelay: 30 * time.Second,
	}

	manager, err := NewManager(opts)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	if manager == nil {
		t.Error("Manager should not be nil")
	}

	// 注意：由于需要有效的API令牌，我们不能进行实际的API调用测试
	// 这些测试主要验证结构是否正确创建
}

func TestNewManagerWithInvalidConfig(t *testing.T) {
	// 测试使用无效配置创建Cloudflare管理器（仅测试参数验证）
	testCases := []struct {
		name string
		opts ManagerOptions
	}{
		{
			name: "Empty account ID",
			opts: ManagerOptions{
				AccountID:  "",
				APIToken:   "test-api-token",
				TunnelID:   "",
				TunnelName: "",
			},
		},
		{
			name: "Empty API token",
			opts: ManagerOptions{
				AccountID:  "test-account-id",
				APIToken:   "",
				TunnelID:   "",
				TunnelName: "",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			manager, err := NewManager(tc.opts)
			if err == nil {
				t.Error("Expected error but got none")
			}

			if manager != nil {
				t.Error("Manager should be nil when config is invalid")
			}
		})
	}
}

func TestManagerOptions(t *testing.T) {
	// 测试ManagerOptions结构体
	opts := ManagerOptions{
		AccountID:     "test-account",
		APIToken:      "test-token",
		TunnelID:      "test-tunnel-id",
		TunnelName:    "test-tunnel-name",
		RateLimit:     10,
		MaxRetries:    3,
		RetryDelay:    1 * time.Second,
		MaxRetryDelay: 30 * time.Second,
	}

	if opts.AccountID != "test-account" {
		t.Errorf("Expected AccountID to be 'test-account', got %s", opts.AccountID)
	}

	if opts.APIToken != "test-token" {
		t.Errorf("Expected APIToken to be 'test-token', got %s", opts.APIToken)
	}

	if opts.TunnelID != "test-tunnel-id" {
		t.Errorf("Expected TunnelID to be 'test-tunnel-id', got %s", opts.TunnelID)
	}

	if opts.TunnelName != "test-tunnel-name" {
		t.Errorf("Expected TunnelName to be 'test-tunnel-name', got %s", opts.TunnelName)
	}

	if opts.RateLimit != 10 {
		t.Errorf("Expected RateLimit to be 10, got %d", opts.RateLimit)
	}

	if opts.MaxRetries != 3 {
		t.Errorf("Expected MaxRetries to be 3, got %d", opts.MaxRetries)
	}

	if opts.RetryDelay != 1*time.Second {
		t.Errorf("Expected RetryDelay to be 1s, got %v", opts.RetryDelay)
	}

	if opts.MaxRetryDelay != 30*time.Second {
		t.Errorf("Expected MaxRetryDelay to be 30s, got %v", opts.MaxRetryDelay)
	}
}

func TestGetOrCreateTunnel(t *testing.T) {
	// TODO: 实现GetOrCreateTunnel方法的测试
	// 由于需要有效的Cloudflare账户和API令牌，这部分测试需要在集成测试环境中进行
	t.Log("GetOrCreateTunnel test placeholder")
}

func TestUpdateConfiguration(t *testing.T) {
	// TODO: 实现UpdateConfiguration方法的测试
	// 由于需要有效的Cloudflare账户和API令牌，这部分测试需要在集成测试环境中进行
	t.Log("UpdateConfiguration test placeholder")
}

func TestGetTunnelToken(t *testing.T) {
	// TODO: 实现GetTunnelToken方法的测试
	// 由于需要有效的Cloudflare账户和API令牌，这部分测试需要在集成测试环境中进行
	t.Log("GetTunnelToken test placeholder")
}

func TestUpsertDNSRecords(t *testing.T) {
	// TODO: 实现UpsertDNSRecords方法的测试
	// 由于需要有效的Cloudflare账户和API令牌，这部分测试需要在集成测试环境中进行
	t.Log("UpsertDNSRecords test placeholder")
}

func TestDeleteDNSRecords(t *testing.T) {
	// TODO: 实现DeleteDNSRecords方法的测试
	// 由于需要有效的Cloudflare账户和API令牌，这部分测试需要在集成测试环境中进行
	t.Log("DeleteDNSRecords test placeholder")
}

func TestCallWithRetry_ReturnsRetryableError(t *testing.T) {
	m := &Manager{
		rateLimiter:   rate.NewLimiter(rate.Inf, 0),
		maxRetries:    1,
		retryDelay:    1 * time.Millisecond,
		maxRetryDelay: 1 * time.Millisecond,
	}

	err := m.callWithRetry(context.Background(), func() error {
		return errors.New("server error: 503 service unavailable")
	})

	var re *types.RetryableError
	if !errors.As(err, &re) {
		t.Errorf("expected RetryableError, got %T: %v", err, err)
	}
}

func TestCallWithRetry_ReturnsPermanentError(t *testing.T) {
	m := &Manager{
		rateLimiter:   rate.NewLimiter(rate.Inf, 0),
		maxRetries:    1,
		retryDelay:    1 * time.Millisecond,
		maxRetryDelay: 1 * time.Millisecond,
	}

	err := m.callWithRetry(context.Background(), func() error {
		return errors.New("authentication error: 401 unauthorized")
	})

	var pe *types.PermanentError
	if !errors.As(err, &pe) {
		t.Errorf("expected PermanentError, got %T: %v", err, err)
	}
}

func TestCallWithRetry_ReturnsNilOnSuccess(t *testing.T) {
	m := &Manager{
		rateLimiter:   rate.NewLimiter(rate.Inf, 0),
		maxRetries:    1,
		retryDelay:    1 * time.Millisecond,
		maxRetryDelay: 1 * time.Millisecond,
	}

	err := m.callWithRetry(context.Background(), func() error {
		return nil
	})

	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestIsRetriableError_TypedAPIError(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.cloudflare.com/test", nil)
	cases := []struct {
		name    string
		err     error
		retrial bool
	}{
		{"429 too many requests", &cloudflare.Error{StatusCode: 429, Request: req, Response: &http.Response{StatusCode: 429, Status: "429 Too Many Requests"}}, true},
		{"500 server error", &cloudflare.Error{StatusCode: 500, Request: req, Response: &http.Response{StatusCode: 500, Status: "500 Internal Server Error"}}, true},
		{"503 service unavailable", &cloudflare.Error{StatusCode: 503, Request: req, Response: &http.Response{StatusCode: 503, Status: "503 Service Unavailable"}}, true},
		{"400 bad request (permanent)", &cloudflare.Error{StatusCode: 400, Request: req, Response: &http.Response{StatusCode: 400, Status: "400 Bad Request"}}, false},
		{"401 unauthorized (permanent)", &cloudflare.Error{StatusCode: 401, Request: req, Response: &http.Response{StatusCode: 401, Status: "401 Unauthorized"}}, false},
		{"403 forbidden (permanent)", &cloudflare.Error{StatusCode: 403, Request: req, Response: &http.Response{StatusCode: 403, Status: "403 Forbidden"}}, false},
		{"404 not found (permanent)", &cloudflare.Error{StatusCode: 404, Request: req, Response: &http.Response{StatusCode: 404, Status: "404 Not Found"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isRetriableError(tc.err)
			if got != tc.retrial {
				t.Errorf("isRetriableError(%v) = %v, want %v", tc.err, got, tc.retrial)
			}
		})
	}
}

func TestIsRetriableError_NetworkLayerStrings(t *testing.T) {
	cases := []struct {
		err     error
		retrial bool
	}{
		{errors.New("dial tcp: connection refused"), true},
		{errors.New("i/o timeout"), true},
		{errors.New("unexpected EOF"), true},
		{errors.New("lookup api.cloudflare.com: no such host"), true},
		{errors.New("server error: 503 service unavailable"), true},
		{errors.New("rate limit exceeded"), true},
		// String matching must NOT misclassify 4xx error bodies that happen to
		// contain a status-like number (e.g. a request ID with "500" in it).
		{errors.New("invalid zone id z500 in request"), false},
		{errors.New("authentication failed"), false},
		{errors.New("not found"), false},
	}
	for _, tc := range cases {
		t.Run(tc.err.Error(), func(t *testing.T) {
			got := isRetriableError(tc.err)
			if got != tc.retrial {
				t.Errorf("isRetriableError(%q) = %v, want %v", tc.err, got, tc.retrial)
			}
		})
	}
}

func TestCallWithRetry_TypedAPIError503IsRetried(t *testing.T) {
	m := &Manager{
		rateLimiter:   rate.NewLimiter(rate.Inf, 0),
		maxRetries:    1,
		retryDelay:    1 * time.Millisecond,
		maxRetryDelay: 1 * time.Millisecond,
	}

	req, _ := http.NewRequest("GET", "https://api.cloudflare.com/test", nil)
	apiErr := &cloudflare.Error{
		StatusCode: 503,
		Request:    req,
		Response:   &http.Response{StatusCode: 503, Status: "503 Service Unavailable"},
	}

	err := m.callWithRetry(context.Background(), func() error {
		return apiErr
	})

	var re *types.RetryableError
	if !errors.As(err, &re) {
		t.Errorf("expected RetryableError for 503 typed API error, got %T: %v", err, err)
	}
}

func TestZoneLookupCandidates(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"app.example.com", []string{"app.example.com", "example.com"}},
		{"APP.Example.COM", []string{"app.example.com", "example.com"}},
		{"app.example.co.uk", []string{"app.example.co.uk", "example.co.uk", "co.uk"}},
		{"a.b.c.d.example.com", []string{"a.b.c.d.example.com", "b.c.d.example.com", "c.d.example.com", "d.example.com", "example.com"}},
		{"example.com", []string{"example.com"}},
		{"example.com.", []string{"example.com"}}, // trailing dot stripped
		{"  ", nil},
		{"singlelabel", nil},
		{"", nil},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := zoneLookupCandidates(tc.in)
			if !slicesEqual(got, tc.want) {
				t.Errorf("zoneLookupCandidates(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNormalizeCNAMEContent(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"abc.cfargotunnel.com", "abc.cfargotunnel.com"},
		{"ABC.CFARGOTUNNEL.COM", "abc.cfargotunnel.com"},
		{"Abc.Cfargotunnel.Com.", "abc.cfargotunnel.com"},
		{"abc.cfargotunnel.com..", "abc.cfargotunnel.com."}, // only single trailing dot stripped
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalizeCNAMEContent(tc.in); got != tc.want {
				t.Errorf("normalizeCNAMEContent(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestZoneCacheKeyIsDomain ensures the cache is keyed by derived domain, not hostname.
// Multiple hostnames in the same zone should share one cache entry.
func TestZoneCacheKeyIsDomain(t *testing.T) {
	m := &Manager{
		zoneCache: make(map[string]string),
	}

	// Manually populate cache as getZoneIDForHostname would: key by domain
	m.cacheMu.Lock()
	m.zoneCache["example.com"] = "zone-123"
	m.cacheMu.Unlock()

	for _, hostname := range []string{"app.example.com", "api.example.com", "blog.example.com"} {
		candidates := zoneLookupCandidates(hostname)
		m.cacheMu.RLock()
		var got string
		var found bool
		for _, dom := range candidates {
			if id, ok := m.zoneCache[dom]; ok {
				got = id
				found = true
				break
			}
		}
		m.cacheMu.RUnlock()
		if !found {
			t.Errorf("hostname %q should have hit cache via domain key", hostname)
		}
		if got != "zone-123" {
			t.Errorf("hostname %q got zone %q, want zone-123", hostname, got)
		}
	}

	// Different zone should NOT hit the same cache entry
	candidates := zoneLookupCandidates("app.other.com")
	m.cacheMu.RLock()
	for _, dom := range candidates {
		if _, ok := m.zoneCache[dom]; ok {
			t.Errorf("unexpected cache hit for %q", dom)
		}
	}
	m.cacheMu.RUnlock()
}

func TestMatchExistingCNAME(t *testing.T) {
	records := []dns.RecordResponse{
		{ID: "id-1", Name: "App.Example.com."},
		{ID: "id-2", Name: "other.example.com"},
	}

	// Case-insensitive + trailing dot on both sides
	if rec := matchExistingCNAME(records, "app.example.com"); rec == nil || rec.ID != "id-1" {
		t.Errorf("expected match for app.example.com (case-insensitive), got %+v", rec)
	}
	if rec := matchExistingCNAME(records, "APP.EXAMPLE.COM."); rec == nil || rec.ID != "id-1" {
		t.Errorf("expected match for APP.EXAMPLE.COM., got %+v", rec)
	}

	// No match
	if rec := matchExistingCNAME(records, "missing.example.com"); rec != nil {
		t.Errorf("expected nil for missing hostname, got %+v", rec)
	}

	// Empty input
	if rec := matchExistingCNAME(nil, "app.example.com"); rec != nil {
		t.Errorf("expected nil for empty records, got %+v", rec)
	}
}

func TestCollectDeleteIDs(t *testing.T) {
	records := []dns.RecordResponse{
		{ID: "id-1", Content: "tun-1.cfargotunnel.com"},
		{ID: "id-2", Content: "TUN-1.CFARGOTUNNEL.COM."}, // 大小写与 trailing dot 归一化后应匹配
		{ID: "id-3", Content: "other.cfargotunnel.com"},  // 非本隧道 content：应被跳过
		{ID: "id-4", Content: ""},                        // 空 content：应被跳过
	}
	deletes := collectDeleteIDs(records, "tun-1.cfargotunnel.com")
	if len(deletes) != 2 {
		t.Fatalf("expected 2 delete payloads, got %d", len(deletes))
	}
	if deletes[0].ID.Value != "id-1" || deletes[1].ID.Value != "id-2" {
		t.Errorf("unexpected delete payloads: %+v", deletes)
	}

	// Empty input yields empty (non-nil) slice
	if out := collectDeleteIDs(nil, "tun-1.cfargotunnel.com"); out == nil || len(out) != 0 {
		t.Errorf("expected empty non-nil slice, got %#v", out)
	}
}

// TestCollectDeleteIDs_FiltersForeignContent verifies the defense-in-depth
// content guard: records whose normalized content differs from the tunnel's
// target are never collected for deletion (C5).
func TestCollectDeleteIDs_FiltersForeignContent(t *testing.T) {
	records := []dns.RecordResponse{
		{ID: "mine", Content: "tun-1.cfargotunnel.com."},
		{ID: "theirs", Content: "other-tunnel.cfargotunnel.com"},
	}
	deletes := collectDeleteIDs(records, "tun-1.cfargotunnel.com")
	if len(deletes) != 1 || deletes[0].ID.Value != "mine" {
		t.Fatalf("expected only the matching record to be deleted, got %+v", deletes)
	}
}

// TestGenerateTunnelSecret verifies the generated tunnel secret is a base64
// string that decodes to exactly 32 bytes, as required by Cloudflare's
// create-tunnel API (C1).
func TestGenerateTunnelSecret(t *testing.T) {
	secret, err := generateTunnelSecret()
	if err != nil {
		t.Fatalf("generateTunnelSecret failed: %v", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		t.Fatalf("secret %q is not valid base64: %v", secret, err)
	}
	if len(decoded) != 32 {
		t.Errorf("expected decoded secret to be 32 bytes, got %d (secret %q)", len(decoded), secret)
	}

	// 两次生成不应相同（随机性）
	secret2, err := generateTunnelSecret()
	if err != nil {
		t.Fatalf("generateTunnelSecret failed: %v", err)
	}
	if secret == secret2 {
		t.Error("expected two generated secrets to differ")
	}
}

// TestRetryAfterDelay verifies Retry-After extraction from a 429 typed API
// error, supporting both delta-seconds and HTTP-date formats (C7).
func TestRetryAfterDelay(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://api.cloudflare.com/test", nil)
	mkErr := func(status int, retryAfter string) error {
		resp := &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Header:     make(http.Header),
		}
		if retryAfter != "" {
			resp.Header.Set("Retry-After", retryAfter)
		}
		return &cloudflare.Error{StatusCode: status, Request: req, Response: resp}
	}

	t.Run("429 delta seconds", func(t *testing.T) {
		if got := retryAfterDelay(mkErr(429, "5")); got != 5*time.Second {
			t.Errorf("retryAfterDelay = %v, want 5s", got)
		}
	})
	t.Run("429 delta seconds with whitespace", func(t *testing.T) {
		if got := retryAfterDelay(mkErr(429, " 12 ")); got != 12*time.Second {
			t.Errorf("retryAfterDelay = %v, want 12s", got)
		}
	})
	t.Run("429 HTTP-date", func(t *testing.T) {
		when := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
		got := retryAfterDelay(mkErr(429, when))
		if got < 89*time.Second || got > 91*time.Second {
			t.Errorf("retryAfterDelay = %v, want ~90s", got)
		}
	})
	t.Run("429 without header", func(t *testing.T) {
		if got := retryAfterDelay(mkErr(429, "")); got != 0 {
			t.Errorf("retryAfterDelay = %v, want 0", got)
		}
	})
	t.Run("non-429 status ignores header", func(t *testing.T) {
		if got := retryAfterDelay(mkErr(500, "5")); got != 0 {
			t.Errorf("retryAfterDelay = %v, want 0", got)
		}
	})
	t.Run("invalid value", func(t *testing.T) {
		if got := retryAfterDelay(mkErr(429, "abc")); got != 0 {
			t.Errorf("retryAfterDelay = %v, want 0", got)
		}
	})
	t.Run("past HTTP-date", func(t *testing.T) {
		when := time.Now().Add(-1 * time.Hour).UTC().Format(http.TimeFormat)
		if got := retryAfterDelay(mkErr(429, when)); got != 0 {
			t.Errorf("retryAfterDelay = %v, want 0", got)
		}
	})
	t.Run("non-API error", func(t *testing.T) {
		if got := retryAfterDelay(errors.New("boom")); got != 0 {
			t.Errorf("retryAfterDelay = %v, want 0", got)
		}
	})
}

// TestCallWithRetry_HonorsRetryAfter verifies the retry loop actually waits at
// least the Retry-After duration returned by a 429 before retrying (C7).
func TestCallWithRetry_HonorsRetryAfter(t *testing.T) {
	m := &Manager{
		rateLimiter:   rate.NewLimiter(rate.Inf, 0),
		maxRetries:    1,
		retryDelay:    1 * time.Millisecond,
		maxRetryDelay: 5 * time.Millisecond,
	}

	// Retry-After 以秒为单位；用 1 秒即可验证“至少等待该时长”的机制
	req, _ := http.NewRequest("GET", "https://api.cloudflare.com/test", nil)
	resp := &http.Response{
		StatusCode: 429,
		Status:     "429 Too Many Requests",
		Header:     make(http.Header),
	}
	resp.Header.Set("Retry-After", "1")

	attempts := 0
	start := time.Now()
	err := m.callWithRetry(context.Background(), func() error {
		attempts++
		if attempts == 1 {
			return &cloudflare.Error{StatusCode: 429, Request: req, Response: resp}
		}
		return nil
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
	// full jitter 最大只有 backoff（约 1ms），Retry-After=1s 应主导等待
	if elapsed < 900*time.Millisecond {
		t.Errorf("expected to wait at least Retry-After (1s), waited %v", elapsed)
	}
}
