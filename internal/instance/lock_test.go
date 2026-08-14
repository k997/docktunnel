package instance

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquire_ThenSecondAcquireFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer first.Close()

	_, err = Acquire(path)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second Acquire: expected ErrAlreadyRunning, got %v", err)
	}
}

func TestAcquire_AfterCloseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("reacquire after Close: %v", err)
	}
	defer second.Close()
}

func TestAcquire_CreatesMissingDir(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "does", "not", "exist", "test.lock")

	l, err := Acquire(nested)
	if err != nil {
		t.Fatalf("Acquire with missing dir: %v", err)
	}
	defer l.Close()

	if _, err := os.Stat(nested); err != nil {
		t.Errorf("expected lock file to be created: %v", err)
	}
}

func TestAcquireWithDefault_DefaultsPath(t *testing.T) {
	// Use a temp path to avoid clobbering the real default.
	dir := t.TempDir()
	path := filepath.Join(dir, "instance.lock")

	l, err := AcquireWithDefault(path)
	if err != nil {
		t.Fatalf("AcquireWithDefault: %v", err)
	}
	defer l.Close()
}

func TestAcquire_StampsHolderPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Close()

	got := readHolderPID(l.f)
	if got != os.Getpid() {
		t.Errorf("expected stamped pid=%d, got %d", os.Getpid(), got)
	}
}

func TestAcquire_SecondHolderReportsBlockingPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer first.Close()

	_, err = Acquire(path)
	if err == nil {
		t.Fatal("expected error from second Acquire, got nil")
	}
	// Error message should mention the pid of the first holder.
	if !contains(err.Error(), "pid") {
		t.Errorf("expected error to mention pid, got: %v", err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
