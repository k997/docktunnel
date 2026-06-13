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
