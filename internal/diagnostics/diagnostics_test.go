package diagnostics

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestComputeDiff_Identical(t *testing.T) {
	desired := []RuleView{{Hostname: "a.com"}, {Hostname: "b.com"}}
	actual := []RuleView{{Hostname: "a.com"}, {Hostname: "b.com"}}
	diff := ComputeDiff(desired, actual)
	if len(diff.DesiredOnly) != 0 || len(diff.ActualOnly) != 0 {
		t.Errorf("expected no differences, got %+v", diff)
	}
	if diff.Matching != 2 {
		t.Errorf("expected Matching=2, got %d", diff.Matching)
	}
}

func TestComputeDiff_Disjoint(t *testing.T) {
	desired := []RuleView{{Hostname: "a.com"}}
	actual := []RuleView{{Hostname: "b.com"}}
	diff := ComputeDiff(desired, actual)
	if !reflect.DeepEqual(sorted(diff.DesiredOnly), []string{"a.com"}) {
		t.Errorf("DesiredOnly = %v, want [a.com]", diff.DesiredOnly)
	}
	if !reflect.DeepEqual(sorted(diff.ActualOnly), []string{"b.com"}) {
		t.Errorf("ActualOnly = %v, want [b.com]", diff.ActualOnly)
	}
	if diff.Matching != 0 {
		t.Errorf("expected Matching=0, got %d", diff.Matching)
	}
}

func TestComputeDiff_PartialOverlap(t *testing.T) {
	desired := []RuleView{{Hostname: "a.com"}, {Hostname: "b.com"}, {Hostname: "c.com"}}
	actual := []RuleView{{Hostname: "b.com"}, {Hostname: "d.com"}}
	diff := ComputeDiff(desired, actual)
	if !reflect.DeepEqual(sorted(diff.DesiredOnly), []string{"a.com", "c.com"}) {
		t.Errorf("DesiredOnly = %v, want [a.com c.com]", diff.DesiredOnly)
	}
	if !reflect.DeepEqual(sorted(diff.ActualOnly), []string{"d.com"}) {
		t.Errorf("ActualOnly = %v, want [d.com]", diff.ActualOnly)
	}
	if diff.Matching != 1 {
		t.Errorf("expected Matching=1, got %d", diff.Matching)
	}
}

func TestComputeDiff_EmptySides(t *testing.T) {
	diff := ComputeDiff(nil, nil)
	if len(diff.DesiredOnly) != 0 || len(diff.ActualOnly) != 0 || diff.Matching != 0 {
		t.Errorf("expected zero diff, got %+v", diff)
	}
}

func TestComputeDiff_DuplicatesInInput(t *testing.T) {
	// Duplicate hostnames in input should be deduplicated by the set logic.
	desired := []RuleView{{Hostname: "a.com"}, {Hostname: "a.com"}}
	actual := []RuleView{{Hostname: "a.com"}}
	diff := ComputeDiff(desired, actual)
	if diff.Matching != 1 {
		t.Errorf("expected Matching=1, got %d", diff.Matching)
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestHandler_ReturnsJSON(t *testing.T) {
	snapshot := func() DebugStateResponse {
		return DebugStateResponse{
			Timestamp:    time.Date(2026, 6, 13, 14, 30, 0, 0, time.UTC),
			DesiredState: []RuleView{{Hostname: "a.com", Service: "http://localhost:80"}},
			ActualState:  []RuleView{{Hostname: "a.com", Service: "http://localhost:80"}},
			Source:       "live_cache",
			Diff:         StateDiff{Matching: 1},
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/debug/state", nil)
	rec := httptest.NewRecorder()

	Handler(snapshot).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var resp DebugStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.Source != "live_cache" {
		t.Errorf("Source = %q, want live_cache", resp.Source)
	}
	if len(resp.DesiredState) != 1 || resp.DesiredState[0].Hostname != "a.com" {
		t.Errorf("DesiredState = %+v, want single a.com entry", resp.DesiredState)
	}
}
