package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"docktunnel/internal/state"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

const ledgerTestTunnelID = "t1"

func tunnelContentFor(tunnelID string) string {
	return fmt.Sprintf("%s.cfargotunnel.com", tunnelID)
}

// dnsLedgerMock extends the shared controller mock with an in-memory DNS
// zone, so ledger-gating behaviour can be observed end to end through
// performSync: ListDNSRecords reports the zone, Upsert/Delete mutate it the
// way the real manager would (upsert writes <tunnelID>.cfargotunnel.com).
type dnsLedgerMock struct {
	*mockCloudflareManager

	mu       sync.Mutex
	records  map[string]string // hostname -> CNAME content
	deleted  []string
	upserted []string
}

func newDNSLedgerMock(tunnelID string, records map[string]string) *dnsLedgerMock {
	return &dnsLedgerMock{
		mockCloudflareManager: &mockCloudflareManager{
			tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: tunnelID},
		},
		records: records,
	}
}

func (m *dnsLedgerMock) tunnelContent() string {
	return tunnelContentFor(m.tunnel.ID)
}

func (m *dnsLedgerMock) ListDNSRecords(ctx context.Context) ([]dns.RecordResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []dns.RecordResponse{}
	for name, content := range m.records {
		out = append(out, dns.RecordResponse{Name: name, Content: content, ID: "rec-" + name})
	}
	return out, nil
}

func (m *dnsLedgerMock) DeleteDNSRecords(ctx context.Context, hostnames []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range hostnames {
		delete(m.records, h)
		m.deleted = append(m.deleted, h)
	}
	return nil
}

func (m *dnsLedgerMock) UpsertDNSRecords(ctx context.Context, hostnames []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, h := range hostnames {
		m.records[h] = m.tunnelContent()
		m.upserted = append(m.upserted, h)
	}
	return nil
}

func (m *dnsLedgerMock) snapshot() (map[string]string, []string, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	records := make(map[string]string, len(m.records))
	for k, v := range m.records {
		records[k] = v
	}
	return records, append([]string(nil), m.deleted...), append([]string(nil), m.upserted...)
}

// setDesiredHostnames injects the desired state directly (same package as the
// controller) so tests can drive syncDNSRecords without a full event round trip.
func setDesiredHostnames(ctrl *Controller, hostnames ...string) {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	ctrl.ingressRules = make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, len(hostnames))
	for _, h := range hostnames {
		ctrl.ingressRules[ingressKey(h, "")] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
			Hostname: cloudflare.F(h),
		}
	}
}

// TestDNSDelete_SkipsUnmanagedOwnTunnelRecord: a record pointing at our tunnel
// that the ledger does not know about must survive reconciliation — this is
// the exact scenario that once wiped manually maintained records.
func TestDNSDelete_SkipsUnmanagedOwnTunnelRecord(t *testing.T) {
	mock := newDNSLedgerMock(ledgerTestTunnelID, map[string]string{
		"stale.example.com": tunnelContentFor(ledgerTestTunnelID),
	})
	ctrl := NewController(&mockDockerScanner{}, mock, ControllerOptions{})
	// desired is empty: the record is not backed by any container.

	if err := ctrl.syncer.performSync(context.Background()); err != nil {
		t.Fatalf("performSync failed: %v", err)
	}

	records, deleted, _ := mock.snapshot()
	if _, exists := records["stale.example.com"]; !exists {
		t.Fatal("unmanaged own-tunnel record must NOT be deleted")
	}
	if len(deleted) != 0 {
		t.Fatalf("no deletions expected, got %v", deleted)
	}
	if len(ctrl.stateManager.ManagedDNS()) != 0 {
		t.Fatalf("ledger must stay empty, got %v", ctrl.stateManager.ManagedDNS())
	}
}

// TestDNSDelete_DeletesManagedRecord: a ledger-owned record that left the
// desired state is deleted, and its ledger entry is removed afterwards.
func TestDNSDelete_DeletesManagedRecord(t *testing.T) {
	mock := newDNSLedgerMock(ledgerTestTunnelID, map[string]string{
		"stale.example.com": tunnelContentFor(ledgerTestTunnelID),
	})
	ctrl := NewController(&mockDockerScanner{}, mock, ControllerOptions{})
	ctrl.stateManager.RecordManagedDNS([]string{"stale.example.com"}, tunnelContentFor(ledgerTestTunnelID))

	if err := ctrl.syncer.performSync(context.Background()); err != nil {
		t.Fatalf("performSync failed: %v", err)
	}

	records, deleted, _ := mock.snapshot()
	if _, exists := records["stale.example.com"]; exists {
		t.Fatal("managed record past its desired state should be deleted")
	}
	if len(deleted) != 1 || deleted[0] != "stale.example.com" {
		t.Fatalf("unexpected deletion list %v", deleted)
	}
	if _, still := ctrl.stateManager.ManagedDNS()["stale.example.com"]; still {
		t.Fatal("ledger entry should be forgotten after successful deletion")
	}
}

// TestDNSDelete_NeverTouchesOtherTunnels: records pointing at a different
// tunnel survive; a stale ledger entry for a repointed record is forgotten
// instead of acted upon.
func TestDNSDelete_NeverTouchesOtherTunnels(t *testing.T) {
	mock := newDNSLedgerMock(ledgerTestTunnelID, map[string]string{
		"other.example.com": "other-tunnel.cfargotunnel.com",
	})
	ctrl := NewController(&mockDockerScanner{}, mock, ControllerOptions{})
	// Ledger claims we once managed it — the record was since repointed
	// externally (or the tunnel was recreated). Entry must be forgotten,
	// the remote record must stay.
	ctrl.stateManager.RecordManagedDNS([]string{"other.example.com"}, tunnelContentFor(ledgerTestTunnelID))

	if err := ctrl.syncer.performSync(context.Background()); err != nil {
		t.Fatalf("performSync failed: %v", err)
	}

	records, deleted, _ := mock.snapshot()
	if _, exists := records["other.example.com"]; !exists {
		t.Fatal("records of other tunnels must never be deleted")
	}
	if len(deleted) != 0 {
		t.Fatalf("no deletions expected, got %v", deleted)
	}
	if _, still := ctrl.stateManager.ManagedDNS()["other.example.com"]; still {
		t.Fatal("stale ledger entry (record repointed externally) should be forgotten")
	}
}

// TestDNSUpsert_RecordsOwnership: records the controller creates enter the
// ledger, which is what later makes them deletable.
func TestDNSUpsert_RecordsOwnership(t *testing.T) {
	mock := newDNSLedgerMock(ledgerTestTunnelID, map[string]string{})
	ctrl := NewController(&mockDockerScanner{}, mock, ControllerOptions{})
	setDesiredHostnames(ctrl, "app.example.com")

	if err := ctrl.syncer.performSync(context.Background()); err != nil {
		t.Fatalf("performSync failed: %v", err)
	}

	records, _, upserted := mock.snapshot()
	if records["app.example.com"] != mock.tunnelContent() {
		t.Fatalf("record should exist with tunnel content, zone: %v", records)
	}
	if len(upserted) != 1 {
		t.Fatalf("unexpected upsert list %v", upserted)
	}
	got := ctrl.stateManager.ManagedDNS()
	if got["app.example.com"] != mock.tunnelContent() {
		t.Fatalf("ledger should record ownership, got %v", got)
	}
}

// TestDNSAdopt_DesiredRecordAlreadyPointingAtTunnel: a desired hostname whose
// zone record already points at this tunnel (created manually or by a previous
// controller life) is adopted into the ledger without a redundant write.
func TestDNSAdopt_DesiredRecordAlreadyPointingAtTunnel(t *testing.T) {
	mock := newDNSLedgerMock(ledgerTestTunnelID, map[string]string{
		"app.example.com": tunnelContentFor(ledgerTestTunnelID),
	})
	ctrl := NewController(&mockDockerScanner{}, mock, ControllerOptions{})
	setDesiredHostnames(ctrl, "app.example.com")

	if err := ctrl.syncer.performSync(context.Background()); err != nil {
		t.Fatalf("performSync failed: %v", err)
	}

	records, _, upserted := mock.snapshot()
	if len(upserted) != 0 {
		t.Fatalf("content already correct — no upsert expected, got %v", upserted)
	}
	if records["app.example.com"] != mock.tunnelContent() {
		t.Fatalf("record must be untouched, zone: %v", records)
	}
	if got := ctrl.stateManager.ManagedDNS()["app.example.com"]; got != mock.tunnelContent() {
		t.Fatalf("record should be adopted into the ledger, got %v", ctrl.stateManager.ManagedDNS())
	}
}

// TestDNSLedger_TrailingDotVariants: Cloudflare may return the record content
// with a trailing dot; both directions must still match the ledger.
func TestDNSLedger_TrailingDotVariants(t *testing.T) {
	trailing := tunnelContentFor(ledgerTestTunnelID) + "."
	mock := newDNSLedgerMock(ledgerTestTunnelID, map[string]string{
		"stale.example.com": trailing,
	})
	ctrl := NewController(&mockDockerScanner{}, mock, ControllerOptions{})
	ctrl.stateManager.RecordManagedDNS([]string{"stale.example.com"}, tunnelContentFor(ledgerTestTunnelID))

	if err := ctrl.syncer.performSync(context.Background()); err != nil {
		t.Fatalf("performSync failed: %v", err)
	}

	records, deleted, _ := mock.snapshot()
	if len(deleted) != 1 {
		t.Fatalf("trailing-dot record must still match the ledger and be deleted, got %v", deleted)
	}
	if _, exists := records["stale.example.com"]; exists {
		t.Fatal("record should be gone")
	}
}

// TestDNSLedger_EmptyLedgerIsFailSafe: a controller whose state (and ledger)
// was lost never deletes anything pointing at its tunnel until it has written
// or adopted records again.
func TestDNSLedger_EmptyLedgerIsFailSafe(t *testing.T) {
	mock := newDNSLedgerMock(ledgerTestTunnelID, map[string]string{
		"a.example.com": tunnelContentFor(ledgerTestTunnelID),
		"b.example.com": tunnelContentFor(ledgerTestTunnelID),
	})
	ctrl := NewController(&mockDockerScanner{}, mock, ControllerOptions{})

	if err := ctrl.syncer.performSync(context.Background()); err != nil {
		t.Fatalf("performSync failed: %v", err)
	}

	records, deleted, _ := mock.snapshot()
	if len(deleted) != 0 {
		t.Fatalf("empty ledger must delete nothing, got %v", deleted)
	}
	if len(records) != 2 {
		t.Fatalf("both records must survive, got %v", records)
	}
}

// TestDNSLedger_RestoreFromNilSnapshot: snapshots written before the ledger
// existed load as "nothing managed", never as garbage-everything.
func TestDNSLedger_RestoreFromNilSnapshot(t *testing.T) {
	mgr := state.NewManager(slog.Default())
	mgr.RecordManagedDNS([]string{"x.example.com"}, "t.cfargotunnel.com")

	snap := mgr.GetSnapshot()
	if snap.ManagedDNS["x.example.com"] != "t.cfargotunnel.com" {
		t.Fatalf("snapshot should carry the ledger, got %v", snap.ManagedDNS)
	}

	// Simulate a pre-ledger snapshot: nil ManagedDNS must restore empty.
	snap.ManagedDNS = nil
	mgr2 := state.NewManager(slog.Default())
	mgr2.LoadFromSnapshot(snap)
	if len(mgr2.ManagedDNS()) != 0 {
		t.Fatalf("nil ledger snapshot must restore empty, got %v", mgr2.ManagedDNS())
	}
}
