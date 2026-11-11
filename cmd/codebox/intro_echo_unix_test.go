//go:build !windows

package main

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestWaitForEnterHidesPastedTextOnATerminal pins the round-4 finding on #215.
// Two properties matter when the user pastes Enter together with the Albert
// key:
//
//   - the key must not be printed (it is a credential, and the password prompt
//     has not had a chance to hide it yet);
//   - the key must survive for the next prompt.
//
// Echo is a terminal-driver property, so this runs the real waitForEnter on a
// pty (a pipe has no echo). The queue contract is pinned separately on a pipe.
//
// Timing note: echo can only be off once the read has started, so the paste is
// issued after the reader is under way — which is also what a user does, having
// just been shown the prompt.
func TestWaitForEnterHidesPastedTextOnATerminal(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()

	stdin := os.Stdin
	os.Stdin = tty
	t.Cleanup(func() { os.Stdin = stdin })

	// Collect everything the terminal produces, so echoed bytes are seen.
	var mu sync.Mutex
	var echoed []byte
	go func() {
		buf := make([]byte, 256)
		for {
			_ = ptmx.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				echoed = append(echoed, buf[:n]...)
				mu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}()

	entered := make(chan bool, 1)
	go func() { entered <- waitForEnter() }()

	time.Sleep(300 * time.Millisecond) // let the read start with echo off
	if _, err := ptmx.WriteString("\npasted-key\n"); err != nil {
		t.Fatal(err)
	}

	select {
	case ok := <-entered:
		if !ok {
			t.Fatal("waitForEnter must report the Enter press")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForEnter never returned")
	}
	time.Sleep(300 * time.Millisecond) // let any echo land before asserting

	mu.Lock()
	rendered := string(echoed)
	mu.Unlock()
	if strings.Contains(rendered, "pasted-key") {
		t.Fatalf("the pasted key was echoed to the terminal: %q", rendered)
	}

	// The key must still be queued for the next prompt.
	if err := tty.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Skipf("cannot set a read deadline: %v", err)
	}
	buf := make([]byte, 64)
	n, err := tty.Read(buf)
	if n == 0 || err != nil {
		t.Fatalf("the pasted key was consumed by the intro (n=%d err=%v)", n, err)
	}
	if got := strings.TrimSpace(string(buf[:n])); got != "pasted-key" {
		t.Fatalf("the queued input was altered: %q", got)
	}
}
