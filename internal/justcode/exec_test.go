package justcode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeV2BackendAuthPolicyAndCredentialTransport(t *testing.T) {
	for _, cfg := range []Config{
		{Isolation: IsolationBackend, Username: "legacy-user", Password: "secret-marker"},
		{Isolation: IsolationBackend, Username: DefaultUsername, Password: ""},
	} {
		if err := ValidateOpenCodeBackend(cfg); err == nil || strings.Contains(err.Error(), "secret-marker") {
			t.Fatalf("incompatible auth must fail without the credential: %v", err)
		}
	}
	if err := ValidateOpenCodeBackend(Config{Isolation: IsolationFull}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOpenCodeBackend(Config{Isolation: IsolationBackend, Username: DefaultUsername, Password: "secret-marker"}); err != nil {
		t.Fatal(err)
	}
	env := withEnv([]string{"OPENCODE_PASSWORD=stale", "OPENCODE_SERVER_PASSWORD=legacy"}, OpenCodeClientEnv("secret-marker")...)
	if !envHas(env, "OPENCODE_PASSWORD=secret-marker") || envHas(env, "OPENCODE_PASSWORD=stale") || !envHas(env, OpenCodeDisableUpdateAssignment) {
		t.Fatal("v2 credential must override inherited password and keep update checks disabled")
	}
}

func TestOpenCodeContinueArgsUsesCapabilityOutput(t *testing.T) {
	if got := strings.Join(OpenCodeContinueArgs("-c, --continue continue the last session"), " "); got != "--continue" {
		t.Fatalf("supported help args = %q", got)
	}
	if got := OpenCodeContinueArgs("Options: --session session id"); len(got) != 0 {
		t.Fatalf("unsupported help args = %v, want no continue flag", got)
	}
}

func TestOpenCodeHasSessions(t *testing.T) {
	for _, tt := range []struct {
		name    string
		output  string
		want    bool
		wantErr bool
	}{
		{name: "empty store", output: "", want: false},
		{name: "empty json list", output: "[]", want: false},
		{name: "existing session", output: `[{"id":"ses_123","title":"test"}]`, want: true},
		{name: "invalid session id", output: `[{"id":"dummy"}]`, wantErr: true},
		{name: "invalid json", output: "not-json", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OpenCodeHasSessions(tt.output)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("OpenCodeHasSessions(%q) = (%t, %v), want (%t, error=%t)", tt.output, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestOpenCodeResumeArgsOnlyContinuesExistingSessions(t *testing.T) {
	for _, tt := range []struct {
		name     string
		has      bool
		listErr  error
		help     string
		helpErr  error
		wantArgs string
		wantWarn bool
	}{
		{name: "first launch is plain", has: false},
		{name: "resume previous session", has: true, help: "--continue continue the last session", wantArgs: "--continue"},
		{name: "old CLI falls back", has: true, help: "--session session id", wantWarn: true},
		{name: "session probe failure falls back", listErr: context.DeadlineExceeded, wantWarn: true},
		{name: "help probe failure falls back", has: true, helpErr: context.DeadlineExceeded, wantWarn: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			args, warn := OpenCodeResumeArgs(tt.has, tt.listErr, tt.help, tt.helpErr)
			if strings.Join(args, " ") != tt.wantArgs || warn != tt.wantWarn {
				t.Fatalf("OpenCodeResumeArgs() = (%v, %t), want (%q, %t)", args, warn, tt.wantArgs, tt.wantWarn)
			}
		})
	}
}

func TestOpenCodeRemoteHasSessionsUsesAuthenticatedSessionRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "test-user" || pass != "test-password" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/location" {
			_, _ = w.Write([]byte(`{"project":{"id":"project-123"}}`))
			return
		}
		if r.URL.Path != "/api/session" || r.URL.Query().Get("project") != "project-123" || r.URL.Query().Get("parentID") != "null" {
			t.Errorf("session request must be project scoped: %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"ses_123","title":"existing"}],"cursor":{}}`))
	}))
	defer server.Close()

	has, err := OpenCodeRemoteHasSessions(context.Background(), server.URL, "test-user", "test-password")
	if err != nil || !has {
		t.Fatalf("OpenCodeRemoteHasSessions() = (%t, %v), want (true, nil)", has, err)
	}
}

// TestStartDeliversStdinToDetachedChild is a regression test for a race where a
// detached child read empty stdin: os/exec copies a non-*os.File Stdin through a
// goroutine, and releasing the process let the CLI exit before that goroutine
// finished, so the guest fell back to the default password and an empty key.
func TestStartDeliversStdinToDetachedChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OSStarter exercises Unix detached-process semantics via /bin/sh; the detach path is Tart-only (macOS), so this is not a Windows behavior")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "received")
	script := filepath.Join(dir, "read-stdin.sh")
	// Publish only after the write finishes, so the reader cannot observe the
	// empty file created by shell redirection before printf runs.
	childScript := "#!/bin/sh\nset -eu\nread -r line\nprintf '%s' \"$line\" > " + shellQuote(out+".tmp") +
		"\nmv " + shellQuote(out+".tmp") + " " + shellQuote(out) + "\n"
	if err := os.WriteFile(script, []byte(childScript), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "child.log")

	if err := (OSStarter{}).Start(strings.NewReader("s3cret\n"), logPath, "/bin/sh", script); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(out)
		if err == nil {
			if string(data) != "s3cret" {
				t.Fatalf("detached child received %q, want %q", data, "s3cret")
			}
			return
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(logPath)
			t.Fatalf("detached child never wrote the received payload; log:\n%s", log)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartRejectsOversizedStdin(t *testing.T) {
	dir := t.TempDir()
	payload := strings.Repeat("x", maxStdinPayload+1)
	err := (OSStarter{}).Start(strings.NewReader(payload), filepath.Join(dir, "log"), "/bin/cat")
	if err == nil || !strings.Contains(err.Error(), "stdin payload exceeds") {
		t.Fatalf("expected oversized-payload error, got %v", err)
	}
}

func TestStartHandlesNilStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OSStarter exercises Unix detached-process semantics via /bin/sh; the detach path is Tart-only (macOS), so this is not a Windows behavior")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	// With no stdin, `true` should still start and exit 0; just assert no error
	// from starting the detached process itself.
	if err := (OSStarter{}).Start(nil, filepath.Join(dir, "child.log"), "/bin/sh", "-c", "printf ok > "+out); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(out); err == nil {
			if string(data) != "ok" {
				t.Fatalf("child output = %q", data)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIsBatchFile(t *testing.T) {
	cases := map[string]bool{
		`C:\tools\opencode.cmd`:   true,
		`C:\tools\opencode.bat`:   true,
		`C:\tools\OPENCODE.CMD`:   true,
		`C:\tools\opencode.exe`:   false,
		`C:\tools\opencode`:       false,
		"/usr/local/bin/opencode": false,
	}
	for path, want := range cases {
		if got := isBatchFile(path); got != want {
			t.Errorf("isBatchFile(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestInteractiveCommandNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("non-Windows behavior")
	}
	cmd := interactiveCommand("opencode", "attach", "http://localhost:4096")
	if cmd.Args[0] != "opencode" {
		t.Errorf("off Windows, interactiveCommand should not wrap through cmd.exe; args[0] = %q", cmd.Args[0])
	}
}

// TestInteractiveCommandWindowsBatch exercises the batch-shim path on real
// Windows: a program whose resolved PATH entry is a .cmd must be invoked
// through the command processor, not passed to CreateProcess directly.
func TestInteractiveCommandWindowsBatch(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only batch shim behavior")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "opencode.cmd")
	if err := os.WriteFile(shim, []byte("@echo off\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd := interactiveCommand("opencode", "attach", "http://localhost:4096")
	if filepath.Base(cmd.Args[0]) != "cmd.exe" {
		t.Fatalf("expected cmd.exe wrapper for a .cmd shim; args[0] = %q", cmd.Args[0])
	}
	if len(cmd.Args) < 3 || cmd.Args[1] != "/c" || !strings.EqualFold(cmd.Args[2], shim) {
		t.Errorf("expected `cmd.exe /c <shim>`; args = %v", cmd.Args)
	}
}
