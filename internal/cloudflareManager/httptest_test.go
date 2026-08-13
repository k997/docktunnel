package cloudflareManager

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"docktunnel/pkg/types"
	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/option"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	"golang.org/x/time/rate"
)

// newTestManager builds a Manager wired to a fake Cloudflare API server.
// The server returns canned responses for tunnel/zone/DNS endpoints and
// records the requests it receives into reqLog for assertions.
func newTestManager(t *testing.T, reqLog *syncLog) *Manager {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reqLog.record(r.Method + " " + r.URL.String())
		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			// ZoneListParams{Name} 以嵌套格式 name=... 序列化；兼容两种写法
			zoneName := r.URL.Query().Get("name")
			if zoneName == "" {
				zoneName = r.URL.Query().Get("name.exact")
			}
			if zoneName == "example.com" {
				w.Write([]byte(`{"result":[{"id":"zone-1","name":"example.com","status":"active"}],"success":true,"errors":[],"messages":[]}`))
				return
			}
			w.Write([]byte(`{"result":[],"success":true,"errors":[],"messages":[]}`))
		case r.Method == "GET" && r.URL.Path == "/zones/zone-1/dns_records":
			// RecordListParamsName 使用嵌套点格式：name.exact=...
			name := r.URL.Query().Get("name.exact")
			if name == "" {
				name = r.URL.Query().Get("name")
			}
			if strings.EqualFold(name, "app.example.com") {
				// 已存在记录：名称大小写不同 + trailing dot，验证匹配逻辑
				w.Write([]byte(`{"result":[{"id":"rec-1","type":"CNAME","name":"App.Example.com.","content":"tun-1.cfargotunnel.com","proxiable":true,"proxied":true,"ttl":1,"locked":false,"zone_id":"zone-1","zone_name":"example.com","created_on":"2026-01-01T00:00:00Z","modified_on":"2026-01-01T00:00:00Z","meta":{},"comment":null,"tags":[]}],"success":true,"errors":[],"messages":[]}`))
				return
			}
			w.Write([]byte(`{"result":[],"success":true,"errors":[],"messages":[]}`))
		case r.Method == "POST" && r.URL.Path == "/zones/zone-1/dns_records/batch":
			body, _ := io.ReadAll(r.Body)
			reqLog.recordBatch(body)
			w.Write([]byte(`{"result":{},"success":true,"errors":[],"messages":[]}`))
		case r.Method == "PUT" && r.URL.Path == "/accounts/acc/cfd_tunnel/tun-1/configurations":
			body, _ := io.ReadAll(r.Body)
			reqLog.recordBatch(body)
			w.Write([]byte(`{"result":{},"success":true,"errors":[],"messages":[]}`))
		case r.Method == "GET" && r.URL.Path == "/accounts/acc/cfd_tunnel/tun-1/configurations":
			w.Write([]byte(`{"result":{"config":{"ingress":[{"hostname":"app.example.com","service":"http://10.0.0.5:8080"}]}},"success":true,"errors":[],"messages":[]}`))
		default:
			http.Error(w, "unhandled: "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	return &Manager{
		client:          cloudflare.NewClient(option.WithAPIToken("tok"), option.WithBaseURL(srv.URL)),
		account:         "acc",
		zoneCache:       make(map[string]string),
		zoneCacheExpiry: make(map[string]time.Time),
		maxRetries:      1,
		retryDelay:      1 * time.Millisecond,
		maxRetryDelay:   5 * time.Millisecond,
		rateLimiter:     rate.NewLimiter(rate.Inf, 0),
		tunnel:          &zero_trust.TunnelCloudflaredGetResponse{ID: "tun-1"},
	}
}

// syncLog is a goroutine-safe request recorder shared between the fake
// server (goroutine) and the test (main goroutine).
type syncLog struct {
	mu       sync.Mutex
	requests []string
	batches  [][]byte
}

func (l *syncLog) record(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, s)
}

func (l *syncLog) recordBatch(body []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.batches = append(l.batches, body)
}

func (l *syncLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.requests))
	copy(out, l.requests)
	return out
}

func (l *syncLog) lastBatch() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.batches) == 0 {
		return nil
	}
	return l.batches[len(l.batches)-1]
}

func TestUpsertDNSRecords_EndToEnd(t *testing.T) {
	log := &syncLog{}
	m := newTestManager(t, log)

	err := m.UpsertDNSRecords(context.Background(), []string{"app.example.com", "new.example.com"})
	if err != nil {
		t.Fatalf("UpsertDNSRecords failed: %v", err)
	}

	// app.example.com 已存在（大小写/点差异）→ Patch；new.example.com 不存在 → Post
	body := log.lastBatch()
	if body == nil {
		t.Fatal("expected a DNS batch request, got none")
	}
	var batch struct {
		Posts   []json.RawMessage `json:"posts"`
		Patches []json.RawMessage `json:"patches"`
	}
	if err := json.Unmarshal(body, &batch); err != nil {
		t.Fatalf("batch body not valid JSON: %v\n%s", err, body)
	}
	if len(batch.Posts) != 1 || len(batch.Patches) != 1 {
		t.Fatalf("expected 1 post + 1 patch, got posts=%d patches=%d body=%s",
			len(batch.Posts), len(batch.Patches), body)
	}

	// 验证 patch 指向已有记录 rec-1
	var patch struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(batch.Patches[0], &patch); err != nil {
		t.Fatal(err)
	}
	if patch.ID != "rec-1" {
		t.Errorf("expected patch on rec-1, got %q", patch.ID)
	}

	// 验证 zone 解析命中 example.com（public suffix 逐级尝试）
	reqs := log.snapshot()
	var sawZoneQuery bool
	for _, req := range reqs {
		if strings.Contains(req, "/zones?") {
			sawZoneQuery = true
		}
	}
	if !sawZoneQuery {
		t.Errorf("expected at least one zone lookup, requests: %v", reqs)
	}
}

func TestUpsertDNSRecords_CreatesWhenMissing(t *testing.T) {
	log := &syncLog{}
	m := newTestManager(t, log)

	err := m.UpsertDNSRecords(context.Background(), []string{"new.example.com"})
	if err != nil {
		t.Fatalf("UpsertDNSRecords failed: %v", err)
	}

	body := log.lastBatch()
	var batch struct {
		Posts   []json.RawMessage `json:"posts"`
		Patches []json.RawMessage `json:"patches"`
	}
	if err := json.Unmarshal(body, &batch); err != nil {
		t.Fatalf("batch body invalid: %v", err)
	}
	if len(batch.Posts) != 1 || len(batch.Patches) != 0 {
		t.Fatalf("expected 1 post + 0 patches, got posts=%d patches=%d body=%s",
			len(batch.Posts), len(batch.Patches), body)
	}
}

func TestDeleteDNSRecords_EndToEnd(t *testing.T) {
	log := &syncLog{}
	m := newTestManager(t, log)

	err := m.DeleteDNSRecords(context.Background(), []string{"app.example.com"})
	if err != nil {
		t.Fatalf("DeleteDNSRecords failed: %v", err)
	}

	body := log.lastBatch()
	var batch struct {
		Deletes []struct {
			ID string `json:"id"`
		} `json:"deletes"`
	}
	if err := json.Unmarshal(body, &batch); err != nil {
		t.Fatalf("batch body invalid: %v\n%s", err, body)
	}
	if len(batch.Deletes) != 1 || batch.Deletes[0].ID != "rec-1" {
		t.Fatalf("expected delete of rec-1, got %+v", batch.Deletes)
	}
}

func TestUpdateConfiguration_EndToEnd(t *testing.T) {
	log := &syncLog{}
	m := newTestManager(t, log)

	ingress := []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		{
			Hostname: cloudflare.F("app.example.com"),
			Service:  cloudflare.F("http://10.0.0.5:8080"),
		},
	}
	if err := m.UpdateConfiguration(context.Background(), ingress); err != nil {
		t.Fatalf("UpdateConfiguration failed: %v", err)
	}

	// 更新走 PUT configurations 端点
	reqs := log.snapshot()
	found := false
	for _, req := range reqs {
		if strings.HasPrefix(req, "PUT /") && strings.HasSuffix(req, "/configurations") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected PUT .../configurations request, got: %v", reqs)
	}
}

func TestGetConfiguration_EndToEnd(t *testing.T) {
	log := &syncLog{}
	m := newTestManager(t, log)

	ingress, err := m.GetConfiguration(context.Background())
	if err != nil {
		t.Fatalf("GetConfiguration failed: %v", err)
	}
	if len(ingress) != 1 || ingress[0].Hostname != "app.example.com" {
		t.Fatalf("unexpected ingress: %+v", ingress)
	}
}

// captureLog swaps slog.Default() with a handler that records every emitted
// line, and restores the original handler on test cleanup. Used to assert
// Error-level log output (e.g. duplicate tunnel detection, failed-zone summary).
func captureLog(t *testing.T) *syncLog {
	t.Helper()
	orig := slog.Default()
	lines := &syncLog{}
	h := slog.NewTextHandler(&lockWriter{lines: lines}, nil)
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return lines
}

// lockWriter is an io.Writer that appends each write to a syncLog.
type lockWriter struct {
	lines *syncLog
}

func (w *lockWriter) Write(p []byte) (int, error) {
	w.lines.record(string(p))
	return len(p), nil
}

// newPaginatedManager builds a Manager whose fake server serves:
//   - GET /zones paged: page 1 → zone-a, page 2 → zone-b, page >=3 → empty
//   - GET /zones/zone-a/dns_records paged: page 1 → rec-a1, page 2 → rec-a2 (trailing dot),
//     both CNAME → tun-1.cfargotunnel.com
//   - GET /zones/zone-b/dns_records: page 1 → rec-b1 (CNAME → tun-1); when failZoneB is
//     true the endpoint returns 500 with a JSON error body instead
func newPaginatedManager(t *testing.T, reqLog *syncLog, failZoneB bool) *Manager {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reqLog.record(r.Method + " " + r.URL.String())

		page := r.URL.Query().Get("page")
		if page == "" {
			page = "1"
		}
		pn, _ := strconv.Atoi(page)

		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			// 多页 zone：page1 → zone-a，page2 → zone-b，page3 起为空
			switch pn {
			case 1:
				w.Write([]byte(`{"result":[{"id":"zone-a","name":"a.example.com","status":"active"}],"success":true,"errors":[],"messages":[],"result_info":{"page":1,"per_page":1,"count":1,"total_count":2,"total_pages":2}}`))
			case 2:
				w.Write([]byte(`{"result":[{"id":"zone-b","name":"b.example.com","status":"active"}],"success":true,"errors":[],"messages":[],"result_info":{"page":2,"per_page":1,"count":1,"total_count":2,"total_pages":2}}`))
			default:
				w.Write([]byte(`{"result":[],"success":true,"errors":[],"messages":[]}`))
			}
		case r.Method == "GET" && r.URL.Path == "/zones/zone-a/dns_records":
			// zone-a 的 CNAME 分 2 页，每页 1 条指向 tun-1
			switch pn {
			case 1:
				w.Write([]byte(`{"result":[{"id":"rec-a1","type":"CNAME","name":"a1.a.example.com","content":"tun-1.cfargotunnel.com","zone_id":"zone-a"}],"success":true,"errors":[],"messages":[]}`))
			case 2:
				w.Write([]byte(`{"result":[{"id":"rec-a2","type":"CNAME","name":"a2.a.example.com","content":"tun-1.cfargotunnel.com.","zone_id":"zone-a"}],"success":true,"errors":[],"messages":[]}`))
			default:
				w.Write([]byte(`{"result":[],"success":true,"errors":[],"messages":[]}`))
			}
		case r.Method == "GET" && r.URL.Path == "/zones/zone-b/dns_records":
			if failZoneB {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"zone b exploded"}],"messages":[]}`))
				return
			}
			if pn == 1 {
				w.Write([]byte(`{"result":[{"id":"rec-b1","type":"CNAME","name":"b1.b.example.com","content":"tun-1.cfargotunnel.com","zone_id":"zone-b"}],"success":true,"errors":[],"messages":[]}`))
			} else {
				w.Write([]byte(`{"result":[],"success":true,"errors":[],"messages":[]}`))
			}
		default:
			http.Error(w, "unhandled: "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	return &Manager{
		client:          cloudflare.NewClient(option.WithAPIToken("tok"), option.WithBaseURL(srv.URL)),
		account:         "acc",
		zoneCache:       make(map[string]string),
		zoneCacheExpiry: make(map[string]time.Time),
		maxRetries:      1,
		retryDelay:      1 * time.Millisecond,
		maxRetryDelay:   5 * time.Millisecond,
		rateLimiter:     rate.NewLimiter(rate.Inf, 0),
	}
}

// newTunnelServerManager builds a Manager whose fake server handles the tunnel
// lifecycle endpoints with the given canned behavior. createResult is returned
// by POST /accounts/acc/cfd_tunnel; getResult is returned by
// GET /accounts/acc/cfd_tunnel/{id}. The tunnel list returns beforePages (one
// JSON result array per page) until a create request is observed, and
// afterPages afterwards — mirroring the "re-check after creation" flow.
func newTunnelServerManager(t *testing.T, reqLog *syncLog, createResult, getResult string, beforePages, afterPages []string) *Manager {
	t.Helper()

	var mu sync.Mutex
	created := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reqLog.record(r.Method + " " + r.URL.String())

		switch {
		case r.Method == "GET" && r.URL.Path == "/accounts/acc/tunnels":
			mu.Lock()
			pages := beforePages
			if created {
				pages = afterPages
			}
			mu.Unlock()
			page := r.URL.Query().Get("page")
			idx := 0
			if page != "" {
				idx, _ = strconv.Atoi(page)
				idx-- // pages are 1-based
			}
			if idx < 0 || idx >= len(pages) {
				w.Write([]byte(`{"result":[],"success":true,"errors":[],"messages":[]}`))
				return
			}
			w.Write([]byte(`{"result":` + pages[idx] + `,"success":true,"errors":[],"messages":[]}`))
		case r.Method == "POST" && r.URL.Path == "/accounts/acc/cfd_tunnel":
			mu.Lock()
			created = true
			mu.Unlock()
			w.Write([]byte(`{"result":` + createResult + `,"success":true,"errors":[],"messages":[]}`))
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/accounts/acc/cfd_tunnel/"):
			w.Write([]byte(`{"result":` + getResult + `,"success":true,"errors":[],"messages":[]}`))
		default:
			http.Error(w, "unhandled: "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	return &Manager{
		client:          cloudflare.NewClient(option.WithAPIToken("tok"), option.WithBaseURL(srv.URL)),
		account:         "acc",
		zoneCache:       make(map[string]string),
		zoneCacheExpiry: make(map[string]time.Time),
		maxRetries:      1,
		retryDelay:      1 * time.Millisecond,
		maxRetryDelay:   5 * time.Millisecond,
		rateLimiter:     rate.NewLimiter(rate.Inf, 0),
	}
}

// TestGetZoneIDForHostname_CacheTTL verifies that an expired zone cache entry
// is treated as a miss and triggers a re-query, while a fresh entry is served
// from cache without extra requests (C8).
func TestGetZoneIDForHostname_CacheTTL(t *testing.T) {
	log := &syncLog{}
	m := newTestManager(t, log)

	// 预置一条已过期的缓存项：过期后应重新查询并返回 zone-1
	m.cacheMu.Lock()
	m.zoneCache["example.com"] = "stale-zone"
	m.zoneCacheExpiry["example.com"] = time.Now().Add(-1 * time.Minute)
	m.cacheMu.Unlock()

	zoneID, err := m.getZoneIDForHostname(context.Background(), "app.example.com")
	if err != nil {
		t.Fatalf("getZoneIDForHostname failed: %v", err)
	}
	if zoneID != "zone-1" {
		t.Fatalf("expected re-query to return zone-1, got %q", zoneID)
	}
	before := len(log.snapshot())

	// 再次调用（同 domain 另一子域）应命中新写入的有效缓存，不再发起 zone 查询
	zoneID, err = m.getZoneIDForHostname(context.Background(), "api.example.com")
	if err != nil {
		t.Fatalf("second getZoneIDForHostname failed: %v", err)
	}
	if zoneID != "zone-1" {
		t.Fatalf("expected cached zone-1, got %q", zoneID)
	}
	if grew := len(log.snapshot()) - before; grew != 0 {
		t.Errorf("expected cache hit with no new requests, got %d new requests", grew)
	}
}

// TestListDNSRecords_PaginatedZonesAndRecords verifies ListDNSRecords walks
// every page of both the zone list and each zone's DNS record list (C3).
func TestListDNSRecords_PaginatedZonesAndRecords(t *testing.T) {
	log := &syncLog{}
	m := newPaginatedManager(t, log, false)
	m.tunnel = &zero_trust.TunnelCloudflaredGetResponse{ID: "tun-1"}

	records, err := m.ListDNSRecords(context.Background())
	if err != nil {
		t.Fatalf("ListDNSRecords failed: %v", err)
	}
	// zone-a 2 页 + zone-b 1 页，共 3 条指向 tun-1 的记录
	if len(records) != 3 {
		t.Fatalf("expected 3 tunnel records across paginated zones, got %d: %+v", len(records), records)
	}
	ids := map[string]bool{}
	for _, r := range records {
		ids[r.ID] = true
	}
	for _, want := range []string{"rec-a1", "rec-a2", "rec-b1"} {
		if !ids[want] {
			t.Errorf("missing record %s in results: %+v", want, records)
		}
	}

	// 断言确实发生了翻页：zone 列表与 DNS 列表都请求过 page=2
	reqs := log.snapshot()
	var sawZonePage2, sawDNSPage2 bool
	for _, req := range reqs {
		if strings.HasPrefix(req, "GET /zones?") && strings.Contains(req, "page=2") {
			sawZonePage2 = true
		}
		if strings.Contains(req, "/dns_records") && strings.Contains(req, "page=2") {
			sawDNSPage2 = true
		}
	}
	if !sawZonePage2 {
		t.Errorf("expected zone list to paginate to page 2, requests: %v", reqs)
	}
	if !sawDNSPage2 {
		t.Errorf("expected DNS records list to paginate to page 2, requests: %v", reqs)
	}
}

// TestListDNSRecords_ContinuesAfterZoneFailure verifies that a single zone's
// DNS listing failure is recorded, other zones are still processed, and one
// summary Error log is emitted — without changing the return value semantics
// (records from healthy zones are still returned, no error) (C3).
func TestListDNSRecords_ContinuesAfterZoneFailure(t *testing.T) {
	log := &syncLog{}
	lines := captureLog(t)
	m := newPaginatedManager(t, log, true) // zone-b 的 DNS 列表返回 500
	m.tunnel = &zero_trust.TunnelCloudflaredGetResponse{ID: "tun-1"}

	records, err := m.ListDNSRecords(context.Background())
	if err != nil {
		t.Fatalf("ListDNSRecords should not fail due to one bad zone, got %v", err)
	}
	if len(records) != 2 { // 仅 zone-a 的两条
		t.Fatalf("expected 2 records from healthy zone-a, got %d: %+v", len(records), records)
	}

	var sawSummary bool
	for _, line := range lines.snapshot() {
		if strings.Contains(line, "Failed to list DNS records for some zones") {
			sawSummary = true
			if !strings.Contains(line, "failedZones=1") {
				t.Errorf("expected summary log to mention failedZones=1, got %q", line)
			}
		}
	}
	if !sawSummary {
		t.Errorf("expected a summary Error log for the failed zone, lines: %v", lines.snapshot())
	}
}

// TestGetOrCreateTunnel_FindsByNameOnSecondPage verifies the by-name tunnel
// lookup walks all pages of the tunnel list (C2).
func TestGetOrCreateTunnel_FindsByNameOnSecondPage(t *testing.T) {
	log := &syncLog{}
	m := newTunnelServerManager(t, log,
		`{"id":"tun-new","name":"DockTunnel"}`,
		`{"id":"tun-b","name":"DockTunnel","account_tag":"acc"}`,
		[]string{
			`[{"id":"tun-a","name":"OtherTunnel","account_tag":"acc"}]`,
			`[{"id":"tun-b","name":"DockTunnel","account_tag":"acc"}]`,
		},
		nil, // 不会创建隧道，afterPages 不生效
	)

	tunnel, err := m.getOrCreateTunnel(context.Background(), "", "DockTunnel")
	if err != nil {
		t.Fatalf("getOrCreateTunnel failed: %v", err)
	}
	if tunnel.ID != "tun-b" {
		t.Fatalf("expected tunnel tun-b (found on page 2), got %+v", tunnel)
	}

	reqs := log.snapshot()
	var sawPage2 bool
	for _, req := range reqs {
		if strings.HasPrefix(req, "GET /accounts/acc/tunnels") && strings.Contains(req, "page=2") {
			sawPage2 = true
		}
	}
	if !sawPage2 {
		t.Errorf("expected tunnel list to paginate to page 2, requests: %v", reqs)
	}
}

// TestGetOrCreateTunnel_CreateThenRecheckByName verifies that after creating a
// tunnel, the manager re-lists by name to confirm exactly one match (C2).
func TestGetOrCreateTunnel_CreateThenRecheckByName(t *testing.T) {
	log := &syncLog{}
	m := newTunnelServerManager(t, log,
		`{"id":"tun-new","name":"DockTunnel"}`,
		`{"id":"tun-new","name":"DockTunnel","account_tag":"acc"}`,
		[]string{`[]`}, // 创建前按名查找：无匹配
		[]string{`[{"id":"tun-new","name":"DockTunnel","account_tag":"acc"}]`}, // 创建后复查：恰好 1 个
	)

	tunnel, err := m.getOrCreateTunnel(context.Background(), "", "DockTunnel")
	if err != nil {
		t.Fatalf("getOrCreateTunnel failed: %v", err)
	}
	if tunnel.ID != "tun-new" {
		t.Fatalf("expected created tunnel tun-new, got %+v", tunnel)
	}

	reqs := log.snapshot()
	var postSeen, listSeen int
	for _, req := range reqs {
		if strings.HasPrefix(req, "POST /accounts/acc/cfd_tunnel") {
			postSeen++
		}
		if strings.HasPrefix(req, "GET /accounts/acc/tunnels") {
			listSeen++
		}
	}
	if postSeen != 1 {
		t.Errorf("expected exactly 1 tunnel create request, got %d", postSeen)
	}
	// 创建前按名查找 + 创建后按名复查 = 至少 2 次列表请求
	if listSeen < 2 {
		t.Errorf("expected by-name re-check after creation (>=2 list requests), got %d", listSeen)
	}
}

// TestGetOrCreateTunnel_DetectsDuplicateAfterCreate verifies that when the
// post-create by-name re-check finds multiple same-name tunnels, an Error log
// is emitted while still returning the first match (C2).
func TestGetOrCreateTunnel_DetectsDuplicateAfterCreate(t *testing.T) {
	log := &syncLog{}
	lines := captureLog(t)
	m := newTunnelServerManager(t, log,
		`{"id":"tun-new","name":"DockTunnel"}`,
		`{"id":"tun-new","name":"DockTunnel","account_tag":"acc"}`,
		[]string{`[]`},
		[]string{`[{"id":"tun-new","name":"DockTunnel","account_tag":"acc"},{"id":"tun-dup","name":"DockTunnel","account_tag":"acc"}]`},
	)

	tunnel, err := m.getOrCreateTunnel(context.Background(), "", "DockTunnel")
	if err != nil {
		t.Fatalf("getOrCreateTunnel failed: %v", err)
	}
	if tunnel.ID != "tun-new" {
		t.Fatalf("expected first duplicate match tun-new, got %+v", tunnel)
	}

	var sawDup bool
	for _, line := range lines.snapshot() {
		if strings.Contains(line, "Duplicate tunnels detected after creation") {
			sawDup = true
			if !strings.Contains(line, "count=2") {
				t.Errorf("expected duplicate log to mention count=2, got %q", line)
			}
		}
	}
	if !sawDup {
		t.Errorf("expected an Error log about duplicate tunnels, lines: %v", lines.snapshot())
	}
}

// TestUpsertDNSRecords_SkipsForeignContent verifies that an existing CNAME
// whose content points at another target is neither patched nor re-created —
// the hostname is skipped entirely (C4).
func TestUpsertDNSRecords_SkipsForeignContent(t *testing.T) {
	log := &syncLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		log.record(r.Method + " " + r.URL.String())
		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			zoneName := r.URL.Query().Get("name")
			if zoneName == "" {
				zoneName = r.URL.Query().Get("name.exact")
			}
			if zoneName == "example.com" {
				w.Write([]byte(`{"result":[{"id":"zone-1","name":"example.com","status":"active"}],"success":true,"errors":[],"messages":[]}`))
				return
			}
			w.Write([]byte(`{"result":[],"success":true,"errors":[],"messages":[]}`))
		case r.Method == "GET" && r.URL.Path == "/zones/zone-1/dns_records":
			// app.example.com 已存在，但 content 指向其它隧道
			name := r.URL.Query().Get("name.exact")
			if name == "" {
				name = r.URL.Query().Get("name")
			}
			if strings.EqualFold(name, "app.example.com") {
				w.Write([]byte(`{"result":[{"id":"rec-1","type":"CNAME","name":"app.example.com","content":"other-tunnel.cfargotunnel.com","zone_id":"zone-1"}],"success":true,"errors":[],"messages":[]}`))
				return
			}
			w.Write([]byte(`{"result":[],"success":true,"errors":[],"messages":[]}`))
		case r.Method == "POST" && r.URL.Path == "/zones/zone-1/dns_records/batch":
			body, _ := io.ReadAll(r.Body)
			log.recordBatch(body)
			w.Write([]byte(`{"result":{},"success":true,"errors":[],"messages":[]}`))
		default:
			http.Error(w, "unhandled: "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	m := &Manager{
		client:          cloudflare.NewClient(option.WithAPIToken("tok"), option.WithBaseURL(srv.URL)),
		account:         "acc",
		zoneCache:       make(map[string]string),
		zoneCacheExpiry: make(map[string]time.Time),
		maxRetries:      1,
		retryDelay:      1 * time.Millisecond,
		maxRetryDelay:   5 * time.Millisecond,
		rateLimiter:     rate.NewLimiter(rate.Inf, 0),
		tunnel:          &zero_trust.TunnelCloudflaredGetResponse{ID: "tun-1"},
	}

	err := m.UpsertDNSRecords(context.Background(), []string{"app.example.com", "new.example.com"})
	if err != nil {
		t.Fatalf("UpsertDNSRecords failed: %v", err)
	}

	// app.example.com 应被跳过；new.example.com 走 POST → 仅 1 批、仅 1 条 post
	body := log.lastBatch()
	if body == nil {
		t.Fatal("expected a DNS batch request for new.example.com, got none")
	}
	var batch struct {
		Posts   []json.RawMessage `json:"posts"`
		Patches []json.RawMessage `json:"patches"`
	}
	if err := json.Unmarshal(body, &batch); err != nil {
		t.Fatalf("batch body invalid: %v\n%s", err, body)
	}
	if len(batch.Posts) != 1 || len(batch.Patches) != 0 {
		t.Fatalf("expected only 1 post and no patches (app.example.com skipped), got posts=%d patches=%d body=%s",
			len(batch.Posts), len(batch.Patches), body)
	}
}

// TestDeleteDNSRecords_SkipsForeignContent verifies records whose content does
// not match this tunnel's target are never deleted (C5).
func TestDeleteDNSRecords_SkipsForeignContent(t *testing.T) {
	log := &syncLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		log.record(r.Method + " " + r.URL.String())
		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			zoneName := r.URL.Query().Get("name")
			if zoneName == "" {
				zoneName = r.URL.Query().Get("name.exact")
			}
			if zoneName == "example.com" {
				w.Write([]byte(`{"result":[{"id":"zone-1","name":"example.com","status":"active"}],"success":true,"errors":[],"messages":[]}`))
				return
			}
			w.Write([]byte(`{"result":[],"success":true,"errors":[],"messages":[]}`))
		case r.Method == "GET" && r.URL.Path == "/zones/zone-1/dns_records":
			name := r.URL.Query().Get("name.exact")
			if name == "" {
				name = r.URL.Query().Get("name")
			}
			if strings.EqualFold(name, "app.example.com") {
				// 同名但 content 指向其它隧道：不得删除
				w.Write([]byte(`{"result":[{"id":"rec-1","type":"CNAME","name":"app.example.com","content":"other-tunnel.cfargotunnel.com","zone_id":"zone-1"}],"success":true,"errors":[],"messages":[]}`))
				return
			}
			w.Write([]byte(`{"result":[],"success":true,"errors":[],"messages":[]}`))
		case r.Method == "POST" && r.URL.Path == "/zones/zone-1/dns_records/batch":
			body, _ := io.ReadAll(r.Body)
			log.recordBatch(body)
			w.Write([]byte(`{"result":{},"success":true,"errors":[],"messages":[]}`))
		default:
			http.Error(w, "unhandled: "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	m := &Manager{
		client:          cloudflare.NewClient(option.WithAPIToken("tok"), option.WithBaseURL(srv.URL)),
		account:         "acc",
		zoneCache:       make(map[string]string),
		zoneCacheExpiry: make(map[string]time.Time),
		maxRetries:      1,
		retryDelay:      1 * time.Millisecond,
		maxRetryDelay:   5 * time.Millisecond,
		rateLimiter:     rate.NewLimiter(rate.Inf, 0),
		tunnel:          &zero_trust.TunnelCloudflaredGetResponse{ID: "tun-1"},
	}

	err := m.DeleteDNSRecords(context.Background(), []string{"app.example.com"})
	if err != nil {
		t.Fatalf("DeleteDNSRecords failed: %v", err)
	}

	// 没有收集到任何可删除记录 → 不应发起任何 batch 删除请求
	for _, req := range log.snapshot() {
		if strings.Contains(req, "/dns_records/batch") {
			t.Errorf("expected no batch delete request when all records point elsewhere, got %s", req)
		}
	}
}

// newAllZonesFailManager builds a Manager whose fake server serves two zones,
// both of which fail their DNS record listing with HTTP 500 — used to verify
// that ListDNSRecords reports an aggregated error when every zone fails (P3-4).
// maxRetries=0 keeps the retry loop at a single attempt.
func newAllZonesFailManager(t *testing.T, reqLog *syncLog) *Manager {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reqLog.record(r.Method + " " + r.URL.String())
		switch {
		case r.Method == "GET" && r.URL.Path == "/zones":
			// SDK 的 V4PagePaginationArray 翻页逻辑只看"本页 result 是否为空"：
			// 第 2 页起必须返回空数组，否则会无限翻页。
			page := r.URL.Query().Get("page")
			if page == "" || page == "1" {
				w.Write([]byte(`{"result":[{"id":"zone-a","name":"a.example.com","status":"active"},{"id":"zone-b","name":"b.example.com","status":"active"}],"success":true,"errors":[],"messages":[]}`))
				return
			}
			w.Write([]byte(`{"result":[],"success":true,"errors":[],"messages":[]}`))
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/zones/") && strings.HasSuffix(r.URL.Path, "/dns_records"):
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"dns listing exploded"}],"messages":[]}`))
		default:
			http.Error(w, "unhandled: "+r.Method+" "+r.URL.Path, http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)

	return &Manager{
		client:          cloudflare.NewClient(option.WithAPIToken("tok"), option.WithBaseURL(srv.URL)),
		account:         "acc",
		zoneCache:       make(map[string]string),
		zoneCacheExpiry: make(map[string]time.Time),
		maxRetries:      0,
		retryDelay:      1 * time.Millisecond,
		maxRetryDelay:   5 * time.Millisecond,
		rateLimiter:     rate.NewLimiter(rate.Inf, 0),
		tunnel:          &zero_trust.TunnelCloudflaredGetResponse{ID: "tun-1"},
	}
}

// TestListDNSRecords_AllZonesFailReturnsError verifies that when every zone's
// DNS listing fails, ListDNSRecords returns an aggregated error instead of a
// silent "success" with an empty result — the outer callWithRetry then retries
// the whole operation with backoff (P3-4).
func TestListDNSRecords_AllZonesFailReturnsError(t *testing.T) {
	log := &syncLog{}
	m := newAllZonesFailManager(t, log)

	records, err := m.ListDNSRecords(context.Background())
	if err == nil {
		t.Fatal("expected an error when all zones fail, got nil (silent success)")
	}
	if len(records) != 0 {
		t.Errorf("expected no records when all zones fail, got %+v", records)
	}
	if !strings.Contains(err.Error(), "failed to list DNS records for all") {
		t.Errorf("expected aggregated error mentioning all-zone failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "a.example.com") {
		t.Errorf("expected aggregated error to name the first failed zone, got: %v", err)
	}

	// 两个 zone 都应被遍历并各自失败
	reqs := log.snapshot()
	var zoneAFails, zoneBFails int
	for _, req := range reqs {
		if strings.Contains(req, "/zones/zone-a/dns_records") {
			zoneAFails++
		}
		if strings.Contains(req, "/zones/zone-b/dns_records") {
			zoneBFails++
		}
	}
	if zoneAFails < 1 || zoneBFails < 1 {
		t.Errorf("expected both zones to be attempted, got zone-a=%d zone-b=%d requests: %v",
			zoneAFails, zoneBFails, reqs)
	}
}

// TestListDNSRecords_AllZonesFailIsRetried verifies the aggregated all-zones
// error is retriable, so callWithRetry retries the whole operation instead of
// classifying it permanent.
func TestListDNSRecords_AllZonesFailIsRetried(t *testing.T) {
	log := &syncLog{}
	m := newAllZonesFailManager(t, log)

	// maxRetries 覆盖为 1：第一次失败后应重试一次（第二次仍失败 → 返回错误）
	m.maxRetries = 1
	_, err := m.ListDNSRecords(context.Background())
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	var re *types.RetryableError
	if !errors.As(err, &re) {
		t.Errorf("expected RetryableError so the whole operation is retried, got %T: %v", err, err)
	}
	reqs := log.snapshot()
	attempts := 0
	for _, req := range reqs {
		if strings.Contains(req, "/zones/zone-a/dns_records") {
			attempts++
		}
	}
	if attempts < 2 {
		t.Errorf("expected the whole operation to be retried (>=2 attempts), got %d: %v", attempts, reqs)
	}
}

// TestUpsertDNSRecords_ChunkedBatches verifies that more than maxBatchSize
// hostnames are submitted as multiple batch calls of at most 200 items (C9).
func TestUpsertDNSRecords_ChunkedBatches(t *testing.T) {
	log := &syncLog{}
	m := newTestManager(t, log)

	hostnames := make([]string, 250)
	for i := range hostnames {
		hostnames[i] = "h" + strconv.Itoa(i) + ".example.com"
	}

	err := m.UpsertDNSRecords(context.Background(), hostnames)
	if err != nil {
		t.Fatalf("UpsertDNSRecords failed: %v", err)
	}

	// 250 条 → 2 批（200 + 50）
	if len(log.batches) != 2 {
		t.Fatalf("expected 2 batch calls for 250 hostnames, got %d", len(log.batches))
	}
	for i, body := range log.batches {
		var batch struct {
			Posts   []json.RawMessage `json:"posts"`
			Patches []json.RawMessage `json:"patches"`
		}
		if err := json.Unmarshal(body, &batch); err != nil {
			t.Fatalf("batch %d body invalid: %v", i, err)
		}
		want := maxBatchSize
		if i == 1 {
			want = 50
		}
		if len(batch.Posts) != want {
			t.Errorf("batch %d: expected %d posts, got %d (body %s)", i, want, len(batch.Posts), body)
		}
		if len(batch.Patches) != 0 {
			t.Errorf("batch %d: expected 0 patches, got %d", i, len(batch.Patches))
		}
	}
}
