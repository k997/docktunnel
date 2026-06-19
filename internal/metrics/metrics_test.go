package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordEvent_IncrementsCounter(t *testing.T) {
	// Reset by checking delta — counter starts at 0 for fresh label set
	before := testutil.ToFloat64(EventsTotal.WithLabelValues("start", "success"))
	RecordEvent("start", "success")
	after := testutil.ToFloat64(EventsTotal.WithLabelValues("start", "success"))
	if after-before != 1 {
		t.Errorf("expected delta=1, got %v", after-before)
	}
}

func TestObserveReconcile_RecordsDuration(t *testing.T) {
	// Histograms don't expose count via ToFloat64 directly; verify no panic.
	ObserveReconcile(0.123)
}

func TestRecordDNSSync_IncrementsCounter(t *testing.T) {
	before := testutil.ToFloat64(DNSSyncOperations.WithLabelValues("upsert", "success"))
	RecordDNSSync("upsert", "success")
	after := testutil.ToFloat64(DNSSyncOperations.WithLabelValues("upsert", "success"))
	if after-before != 1 {
		t.Errorf("expected delta=1, got %v", after-before)
	}
}

func TestSetRetentionEntries_SetsGauge(t *testing.T) {
	SetRetentionEntries("Active", 5)
	if got := testutil.ToFloat64(RetentionEntries.WithLabelValues("Active")); got != 5 {
		t.Errorf("expected 5, got %v", got)
	}
}

func TestSetCompensationQueueLength_SetsGauge(t *testing.T) {
	SetCompensationQueueLength(7)
	if got := testutil.ToFloat64(CompensationQueueLength); got != 7 {
		t.Errorf("expected 7, got %v", got)
	}
}

func TestIncCompensationQueueOverflow_IncrementsCounter(t *testing.T) {
	before := testutil.ToFloat64(CompensationQueueOverflow)
	IncCompensationQueueOverflow()
	IncCompensationQueueOverflow()
	after := testutil.ToFloat64(CompensationQueueOverflow)
	if after-before != 2 {
		t.Errorf("expected delta=2, got %v", after-before)
	}
}

func TestAddGCDeletions_AddsToCounter(t *testing.T) {
	before := testutil.ToFloat64(GCDeletions)
	AddGCDeletions(5)
	after := testutil.ToFloat64(GCDeletions)
	if after-before != 5 {
		t.Errorf("expected delta=5, got %v", after-before)
	}
}

func TestAddGCDeletions_IgnoresNonPositive(t *testing.T) {
	before := testutil.ToFloat64(GCDeletions)
	AddGCDeletions(0)
	AddGCDeletions(-3)
	after := testutil.ToFloat64(GCDeletions)
	if after != before {
		t.Errorf("expected no change, got delta=%v", after-before)
	}
}

func TestObserveSyncDuration_RecordsDuration(t *testing.T) {
	ObserveSyncDuration(0.456)
}

// TestRecordEvent_BoundsResultCardinality verifies that an arbitrary string
// (e.g. err.Error() containing a Cloudflare request ID) collapses to the
// "unknown" bucket instead of creating a new time series per request.
func TestRecordEvent_BoundsResultCardinality(t *testing.T) {
	beforeUnknown := testutil.ToFloat64(EventsTotal.WithLabelValues("start", "unknown"))
	beforeGarbage := testutil.ToFloat64(EventsTotal.WithLabelValues("start", "some-cloudflare-request-id-abc123"))

	RecordEvent("start", "some-cloudflare-request-id-abc123")

	afterUnknown := testutil.ToFloat64(EventsTotal.WithLabelValues("start", "unknown"))
	afterGarbage := testutil.ToFloat64(EventsTotal.WithLabelValues("start", "some-cloudflare-request-id-abc123"))

	if afterUnknown-beforeUnknown != 1 {
		t.Errorf("expected unknown bucket to absorb the bad label by +1, got delta=%v", afterUnknown-beforeUnknown)
	}
	if afterGarbage-beforeGarbage != 0 {
		t.Errorf("expected no new series for arbitrary result string, got delta=%v", afterGarbage-beforeGarbage)
	}
}

// TestRecordEvent_BoundsEventTypeCardinality verifies the same protection
// for the "type" label.
func TestRecordEvent_BoundsEventTypeCardinality(t *testing.T) {
	beforeUnknown := testutil.ToFloat64(EventsTotal.WithLabelValues("unknown", "success"))
	beforeGarbage := testutil.ToFloat64(EventsTotal.WithLabelValues("some-future-event-type", "success"))

	RecordEvent("some-future-event-type", "success")

	afterUnknown := testutil.ToFloat64(EventsTotal.WithLabelValues("unknown", "success"))
	afterGarbage := testutil.ToFloat64(EventsTotal.WithLabelValues("some-future-event-type", "success"))

	if afterUnknown-beforeUnknown != 1 {
		t.Errorf("expected unknown type bucket to absorb by +1, got delta=%v", afterUnknown-beforeUnknown)
	}
	if afterGarbage-beforeGarbage != 0 {
		t.Errorf("expected no new series for arbitrary event type, got delta=%v", afterGarbage-beforeGarbage)
	}
}

// TestRecordEvent_AcceptsAllEnumeratedResults verifies the closed set of
// allowed results all pass through unchanged.
func TestRecordEvent_AcceptsAllEnumeratedResults(t *testing.T) {
	for _, r := range []string{ResultSuccess, ResultFailure, ResultSkipped, ResultDropped} {
		before := testutil.ToFloat64(EventsTotal.WithLabelValues("start", r))
		RecordEvent("start", r)
		after := testutil.ToFloat64(EventsTotal.WithLabelValues("start", r))
		if after-before != 1 {
			t.Errorf("result=%q: expected delta=1, got %v", r, after-before)
		}
	}
}

func TestIncEventsDropped_IncrementsCounter(t *testing.T) {
	before := testutil.ToFloat64(EventsDropped)
	IncEventsDropped()
	IncEventsDropped()
	IncEventsDropped()
	after := testutil.ToFloat64(EventsDropped)
	if after-before != 3 {
		t.Errorf("expected delta=3, got %v", after-before)
	}
}

// TestEventsDroppedCounterValue_MatchesProm tests that the snapshot helper
// used by the main watchdog goroutine stays in sync with the Prometheus
// counter — so the WARN log fires exactly when a scrape would also see it.
func TestEventsDroppedCounterValue_MatchesProm(t *testing.T) {
	IncEventsDropped()
	promVal := testutil.ToFloat64(EventsDropped)
	snapVal := EventsDroppedCounterValue()
	if snapVal != promVal {
		t.Errorf("snapshot=%v but prometheus counter=%v; they must match", snapVal, promVal)
	}
}

func TestSetEventChannelLength_SetsGauge(t *testing.T) {
	SetEventChannelLength(42)
	if got := testutil.ToFloat64(EventChannelLength); got != 42 {
		t.Errorf("expected 42, got %v", got)
	}
}

// TestEventsDroppedDelta is a sanity check on the helper arithmetic.
func TestEventsDroppedDelta(t *testing.T) {
	if got := EventsDroppedDelta(10, 25); got != 15 {
		t.Errorf("expected 15, got %v", got)
	}
	if got := EventsDroppedDelta(25, 10); got != -15 {
		t.Errorf("expected -15, got %v", got)
	}
}
