package types

import "testing"

func TestTransitionEventValues(t *testing.T) {
	events := []TransitionEvent{EventContainerStarted, EventContainerStopped, EventRetentionExpired, EventCleanupComplete}
	for i, e := range events {
		if int(e) != i {
			t.Errorf("TransitionEvent %d has unexpected value %d", i, int(e))
		}
	}
}

func TestActionKindValues(t *testing.T) {
	kinds := []ActionKind{ActionNone, ActionDeleteRoute}
	for i, k := range kinds {
		if int(k) != i {
			t.Errorf("ActionKind %d has unexpected value %d", i, int(k))
		}
	}
}

func TestEntryStatusRetaining(t *testing.T) {
	if StatusRetaining == StatusActive || StatusRetaining == StatusPendingDelete || StatusRetaining == StatusDeleted {
		t.Error("StatusRetaining must be distinct from other statuses")
	}
}

func TestEntryStatus_String(t *testing.T) {
	tests := []struct {
		status EntryStatus
		want   string
	}{
		{StatusActive, "Active"},
		{StatusPendingDelete, "PendingDelete"},
		{StatusRetaining, "Retaining"},
		{StatusDeleted, "Deleted"},
		{StatusFlapping, "Flapping"},
		{EntryStatus(99), "Unknown"},
	}
	for _, tt := range tests {
		got := tt.status.String()
		if got != tt.want {
			t.Errorf("EntryStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}
