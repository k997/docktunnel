// Package diagnostics provides types and helpers for the /debug/state endpoint.
package diagnostics

import (
    "encoding/json"
    "net/http"
    "time"
)

// RuleView is a single ingress rule as exposed in the debug response.
type RuleView struct {
    Hostname string `json:"hostname"`
    Service  string `json:"service"`
    Path     string `json:"path,omitempty"`
}

// StateDiff classifies rules by where they appear relative to desired/actual.
type StateDiff struct {
    DesiredOnly []string `json:"desired_only"`
    ActualOnly  []string `json:"actual_only"`
    Matching    int      `json:"matching"`
}

// DebugStateResponse is the JSON returned by /debug/state.
type DebugStateResponse struct {
    Timestamp    time.Time  `json:"timestamp"`
    DesiredState []RuleView `json:"desired_state"`
    ActualState  []RuleView `json:"actual_state"`
    Source       string     `json:"source"`
    Diff         StateDiff  `json:"diff"`
}

// ComputeDiff returns the set difference of desired vs actual by hostname.
// Duplicate hostnames within a single slice are deduplicated.
func ComputeDiff(desired, actual []RuleView) StateDiff {
    desiredSet := make(map[string]struct{}, len(desired))
    actualSet := make(map[string]struct{}, len(actual))

    for _, r := range desired {
        desiredSet[r.Hostname] = struct{}{}
    }
    for _, r := range actual {
        actualSet[r.Hostname] = struct{}{}
    }

    var diff StateDiff
    for h := range desiredSet {
        if _, ok := actualSet[h]; !ok {
            diff.DesiredOnly = append(diff.DesiredOnly, h)
        } else {
            diff.Matching++
        }
    }
    for h := range actualSet {
        if _, ok := desiredSet[h]; !ok {
            diff.ActualOnly = append(diff.ActualOnly, h)
        }
    }
    return diff
}

// Handler returns an http.HandlerFunc that serves the current debug state.
// The snapshot function is called on every request; it must be safe to call
// from any goroutine.
func Handler(snapshot func() DebugStateResponse) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        resp := snapshot()
        w.Header().Set("Content-Type", "application/json")
        if err := json.NewEncoder(w).Encode(resp); err != nil {
            http.Error(w, "failed to encode response", http.StatusInternalServerError)
        }
    }
}
