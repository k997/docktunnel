package types

import "fmt"

// RetryableError wraps an error that should be retried (e.g., 429, 5xx, timeout).
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string { return fmt.Sprintf("retryable: %v", e.Err) }
func (e *RetryableError) Unwrap() error { return e.Err }

// PermanentError wraps an error that should not be retried (e.g., 401, 403, bad params).
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string { return fmt.Sprintf("permanent: %v", e.Err) }
func (e *PermanentError) Unwrap() error { return e.Err }
