package types

import (
	"errors"
	"testing"
)

func TestRetryableError(t *testing.T) {
	inner := errors.New("connection timeout")
	err := &RetryableError{Err: inner}

	if err.Error() != "retryable: connection timeout" {
		t.Errorf("expected 'retryable: connection timeout', got %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("expected errors.Is to match inner error")
	}
}

func TestPermanentError(t *testing.T) {
	inner := errors.New("403 forbidden")
	err := &PermanentError{Err: inner}

	if err.Error() != "permanent: 403 forbidden" {
		t.Errorf("expected 'permanent: 403 forbidden', got %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("expected errors.Is to match inner error")
	}
}

func TestAsRetryableError(t *testing.T) {
	err := &RetryableError{Err: errors.New("timeout")}
	var re *RetryableError
	if !errors.As(err, &re) {
		t.Error("expected errors.As to match RetryableError")
	}
}

func TestAsPermanentError(t *testing.T) {
	err := &PermanentError{Err: errors.New("bad request")}
	var pe *PermanentError
	if !errors.As(err, &pe) {
		t.Error("expected errors.As to match PermanentError")
	}
}