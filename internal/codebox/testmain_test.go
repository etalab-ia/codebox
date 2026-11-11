package codebox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMain(m *testing.M) {
	stateDir, err := os.MkdirTemp("", "codebox-test-state-")
	if err != nil {
		panic(err)
	}
	projectUpdateStateDirFn = func() string { return stateDir }
	// The bundled secret scanner downloads from its pinned release on first
	// use, which a unit test must never do. A stub that reports no findings
	// keeps every scan path exercised while staying hermetic; tests that need
	// findings inject their own executable (see gitleaks_test.go). Windows has
	// no POSIX shell for the stub, and the finding-producing tests skip there.
	scannerDir, err := os.MkdirTemp("", "codebox-test-scanner-")
	if err != nil {
		panic(err)
	}
	if runtime.GOOS == "windows" {
		gitleaksExecutable = func(context.Context) (string, error) {
			return "", fmt.Errorf("the bundled scanner stub needs a POSIX shell")
		}
	} else {
		stub := filepath.Join(scannerDir, "gitleaks")
		if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '[]'\n"), 0o755); err != nil {
			panic(err)
		}
		gitleaksExecutable = func(context.Context) (string, error) { return stub, nil }
	}
	code := m.Run()
	_ = os.RemoveAll(stateDir)
	_ = os.RemoveAll(scannerDir)
	os.Exit(code)
}
