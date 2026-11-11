package codebox

import "time"

// Retry only classified sharing/access errors, preserving the last underlying
// error at the bound. Callers retry one filesystem operation, not a transaction.
func retryFilesystemOperation(operation func() error, retryable func(error) bool) error {
	delay := 10 * time.Millisecond
	for attempt := 0; ; attempt++ {
		err := operation()
		if err == nil || !retryable(err) || attempt >= 7 {
			return err
		}
		time.Sleep(delay)
		delay *= 2
	}
}
