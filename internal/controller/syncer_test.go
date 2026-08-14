package controller

import (
	"log/slog"
	"testing"

	"docktunnel/internal/diagnostics"
)

func TestSyncer_newSyncer_ReturnsNonNil(t *testing.T) {
	c := &Controller{}
	s := newSyncer(c, slog.Default())
	if s == nil {
		t.Fatal("expected non-nil syncer")
	}
	if s.c == nil {
		t.Error("expected syncer.c to be set")
	}
}

func TestSyncer_setLastKnownActualRules_CachesForDebugState(t *testing.T) {
	c := &Controller{}
	s := newSyncer(c, slog.Default())

	rules := []diagnostics.RuleView{
		{Hostname: "a.example.com", Service: "http://a:8080"},
		{Hostname: "b.example.com", Service: "http://b:8080"},
	}
	s.setLastKnownActualRules(rules)

	s.actualStateMu.RLock()
	got := s.lastKnownActualRules
	s.actualStateMu.RUnlock()

	if len(got) != 2 {
		t.Fatalf("expected 2 cached rules, got %d", len(got))
	}
	if got[0].Hostname != "a.example.com" {
		t.Errorf("expected first hostname a.example.com, got %s", got[0].Hostname)
	}
}

func TestSyncer_setLastKnownActualRules_NilEmptiesCache(t *testing.T) {
	c := &Controller{}
	s := newSyncer(c, slog.Default())

	s.setLastKnownActualRules([]diagnostics.RuleView{{Hostname: "x"}})
	s.setLastKnownActualRules(nil)

	s.actualStateMu.RLock()
	got := s.lastKnownActualRules
	s.actualStateMu.RUnlock()

	if got != nil {
		t.Errorf("expected nil after setting nil, got %v", got)
	}
}
