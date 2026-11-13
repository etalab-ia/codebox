package codebox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestFilesystemRetryClassification(t *testing.T) {
	for _, code := range []syscall.Errno{5, 32, 33} {
		err := &os.PathError{Op: "open", Path: "test", Err: code}
		if isTransientFilesystemError(err) != (runtime.GOOS == "windows") {
			t.Errorf("unexpected platform classification for error code %d", code)
		}
	}
	for _, err := range []error{os.ErrNotExist, errors.New("invalid data")} {
		if isTransientFilesystemError(err) {
			t.Fatal("unrelated errors must not be retried")
		}
	}
}

func TestFilesystemRetryBoundAndOriginalError(t *testing.T) {
	transient := errors.New("injected contention")
	for _, recover := range []bool{true, false} {
		calls := 0
		err := retryFilesystemOperation(func() error {
			calls++
			if recover && calls == 3 {
				return nil
			}
			return fmt.Errorf("operation failed: %w", transient)
		}, func(err error) bool { return errors.Is(err, transient) })
		if recover && (err != nil || calls != 3) {
			t.Fatalf("transient operation did not recover: calls=%d err=%v", calls, err)
		}
		if !recover && (calls != 8 || !errors.Is(err, transient)) {
			t.Fatalf("permanent contention must be bounded and retain cause: calls=%d err=%v", calls, err)
		}
	}
}

type transientCreateFS struct {
	FS
	remaining int
	calls     int
	err       error
}

func (f *transientCreateFS) CreateExclusive(path string, data []byte, mode os.FileMode) error {
	f.calls++
	if f.remaining > 0 {
		f.remaining--
		return f.err
	}
	return f.FS.CreateExclusive(path, data, mode)
}

func TestProjectLockRetriesTransientCreateErrors(t *testing.T) {
	denied := errors.New("injected delete-pending access denial")
	fs := &transientCreateFS{FS: realFS{}, remaining: 2, err: denied}
	lock := &ProjectLock{Path: filepath.Join(t.TempDir(), "lock"), FS: fs,
		WaitTimeout: time.Second, PollInterval: time.Millisecond,
		retryableCreateError: func(err error) bool { return errors.Is(err, denied) }}
	release, err := lock.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	release()
	if fs.calls != 3 {
		t.Fatalf("create attempts=%d, want 3", fs.calls)
	}
	fs.remaining = 10000
	lock.WaitTimeout = 5 * time.Millisecond
	if _, err := lock.Acquire(); !errors.Is(err, denied) {
		t.Fatalf("permanently denied create must retain its error at deadline: %v", err)
	}
}
