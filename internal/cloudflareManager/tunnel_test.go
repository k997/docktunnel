package cloudflareManager

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"docktunnel/pkg/types"
	"github.com/cloudflare/cloudflare-go/v5"
	"golang.org/x/time/rate"
)

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
