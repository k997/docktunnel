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
