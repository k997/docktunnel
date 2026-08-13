package cloudflareManager

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
		client:        cloudflare.NewClient(option.WithAPIToken("tok"), option.WithBaseURL(srv.URL)),
		account:       "acc",
		zoneCache:     make(map[string]string),
		maxRetries:    1,
		retryDelay:    1 * time.Millisecond,
		maxRetryDelay: 5 * time.Millisecond,
		rateLimiter:   rate.NewLimiter(rate.Inf, 0),
		tunnel:        &zero_trust.TunnelCloudflaredGetResponse{ID: "tun-1"},
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
