// Package metrics defines DockTunnel's Prometheus metrics and helper
// functions for instrumented call sites. Importing this package registers
// all metrics with the default Prometheus registry.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Closed label sets for EventsTotal. Anything outside these sets gets bucketed
// into "unknown" rather than producing a new Prometheus time series. This
// matters because callers can plausibly pass err.Error() or arbitrary event
// types — without this guard, a Cloudflare API error containing a request ID
// would inflate cardinality by one series per request.

const (
	resultSuccess = "success"
	resultFailure = "failure"
	resultSkipped = "skipped"
	resultDropped = "dropped"
	resultUnknown = "unknown"
)

var allowedResults = map[string]struct{}{
	resultSuccess: {},
	resultFailure: {},
	resultSkipped: {},
	resultDropped: {},
}

var allowedEventTypes = map[string]struct{}{
	"start":            {},
	"stop":             {},
	"die":              {},
	"destroy":          {},
	"health_healthy":   {},
	"health_unhealthy": {},
	"health_starting":  {},
	"resync":           {},
}

func safeResultLabel(r string) string {
	if _, ok := allowedResults[r]; ok {
		return r
	}
	return resultUnknown
}

func safeEventTypeLabel(t string) string {
	if _, ok := allowedEventTypes[t]; ok {
		return t
	}
	return "unknown"
}

var (
	// EventsTotal counts Docker events processed by Dispatch.
	EventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "docktunnel",
		Name:      "events_total",
		Help:      "Total Docker events processed by type and result.",
	}, []string{"type", "result"})

	// EventsDropped counts Docker events dropped because the event channel
	// was full. A container storm (bulk restarts) can outpace Dispatch, and
	// rather than block the Docker event-listener goroutine — which would
	// eventually cause the daemon to drop events itself — we drop on our
	// side and surface it via this counter. Reconciliation will catch up
	// state on the next resync.
	EventsDropped = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "docktunnel",
		Name:      "events_dropped_total",
		Help:      "Docker events dropped because the internal event channel was full.",
	})

	// ReconcileDuration observes wall-clock time of Reconcile cycles.
	ReconcileDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "docktunnel",
		Name:      "reconcile_duration_seconds",
		Help:      "Wall-clock time spent in Reconcile().",
		Buckets:   prometheus.DefBuckets,
	})

	// DNSSyncOperations counts Cloudflare DNS API calls.
	DNSSyncOperations = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "docktunnel",
		Name:      "dns_sync_operations_total",
		Help:      "DNS sync operations against Cloudflare by operation and result.",
	}, []string{"operation", "result"})

	// RetentionEntries tracks tunnel entries grouped by lifecycle status.
	RetentionEntries = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "docktunnel",
		Name:      "retention_entries",
		Help:      "Number of retention entries by status (Active, Retaining, PendingDelete).",
	}, []string{"status"})

	// CompensationQueueLength tracks the compensation queue depth.
	CompensationQueueLength = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: "docktunnel",
		Name:      "compensation_queue_length",
		Help:      "Number of items currently in the compensation queue.",
	})

	// CompensationQueueOverflow counts enqueues rejected due to queue cap.
	CompensationQueueOverflow = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "docktunnel",
		Name:      "compensation_queue_overflow_total",
		Help:      "Number of compensation enqueues rejected because the queue was at capacity.",
	})

	// GCDeletions counts expired retention entries removed by RunGarbageCollection.
	GCDeletions = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "docktunnel",
		Name:      "gc_deletions_total",
		Help:      "Number of expired retention entries removed by garbage collection.",
	})

	// SyncDuration observes wall-clock time of Cloudflare sync (performSync) cycles.
	SyncDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Namespace: "docktunnel",
		Name:      "sync_duration_seconds",
		Help:      "Wall-clock time spent in performSync() (Cloudflare configuration + DNS sync).",
		Buckets:   prometheus.DefBuckets,
	})
)

// RecordEvent increments EventsTotal with the given labels. Both labels are
// constrained to closed sets; unknown values collapse to "unknown" rather
// than creating a new time series. Use the Result* constants for result.
const (
	ResultSuccess = resultSuccess
	ResultFailure = resultFailure
	ResultSkipped = resultSkipped
	ResultDropped = resultDropped
)

func RecordEvent(eventType, result string) {
	EventsTotal.WithLabelValues(safeEventTypeLabel(eventType), safeResultLabel(result)).Inc()
}

// IncEventsDropped increments the EventsDropped counter by 1.
func IncEventsDropped() {
	EventsDropped.Inc()
}

// ObserveReconcile records a Reconcile cycle's duration in seconds.
func ObserveReconcile(seconds float64) {
	ReconcileDuration.Observe(seconds)
}

// RecordDNSSync increments DNSSyncOperations with the given labels.
func RecordDNSSync(operation, result string) {
	DNSSyncOperations.WithLabelValues(operation, result).Inc()
}

// SetRetentionEntries sets the gauge for the given status.
func SetRetentionEntries(status string, n int) {
	RetentionEntries.WithLabelValues(status).Set(float64(n))
}

// SetCompensationQueueLength sets the compensation queue gauge.
func SetCompensationQueueLength(n int) {
	CompensationQueueLength.Set(float64(n))
}

// IncCompensationQueueOverflow increments the overflow counter by 1.
func IncCompensationQueueOverflow() {
	CompensationQueueOverflow.Inc()
}

// AddGCDeletions adds n to the GC deletions counter.
func AddGCDeletions(n int) {
	if n > 0 {
		GCDeletions.Add(float64(n))
	}
}

// ObserveSyncDuration records a performSync cycle's duration in seconds.
func ObserveSyncDuration(seconds float64) {
	SyncDuration.Observe(seconds)
}
