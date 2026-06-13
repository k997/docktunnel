// Package metrics defines DockTunnel's Prometheus metrics and helper
// functions for instrumented call sites. Importing this package registers
// all metrics with the default Prometheus registry.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// EventsTotal counts Docker events processed by Dispatch.
	EventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "docktunnel",
		Name:      "events_total",
		Help:      "Total Docker events processed by type and result.",
	}, []string{"type", "result"})

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
)

// RecordEvent increments EventsTotal with the given labels.
func RecordEvent(eventType, result string) {
	EventsTotal.WithLabelValues(eventType, result).Inc()
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
