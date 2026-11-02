//go:build !windows

package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"golang.org/x/term"
)

func TestMultiSelectFormsRenderOptionsOnAPTY(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	for _, tc := range multiSelectRenderCases() {
		t.Run(tc.name, func(t *testing.T) {
			for _, height := range []int{8, 24} {
				session := newMultiSelectPTYSession(t, tc.newForm(t), 100, height)
				output := session.waitFor(tc.options[0])
				for _, option := range tc.options {
					if !strings.Contains(output, option) {
						t.Errorf("PTY height %d hides option %q:\n%s", height, option, output)
					}
				}
				if !multiSelectCursorOnOption(output, tc.options[0]) {
					t.Errorf("PTY height %d hides the option cursor:\n%s", height, output)
				}
				session.run("")
			}
		})
	}
}

func TestMultiSelectPTYResizePreservesCursorAndSelection(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	options := make([]huh.Option[string], 12)
	for i := range options {
		label := fmt.Sprintf("mcp-%02d", i)
		options[i] = huh.NewOption(label, label)
	}
	selected := []string{}
	form := huh.NewForm(huh.NewGroup(newProjectMCPsMultiSelect(options, &selected)))
	session := newMultiSelectPTYSession(t, form, 48, 8)
	_ = session.waitFor("mcp-00")
	if _, err := session.ptmx.WriteString(strings.Repeat("\x1b[B", 11) + " "); err != nil {
		t.Fatalf("write navigation keys: %v", err)
	}
	output := session.waitFor("mcp-11")
	if !multiSelectCursorOnOption(output, "mcp-11") || !multiSelectSelectionOnOption(output, "mcp-11") {
		t.Fatalf("PTY scrolling or selection lost the last option:\n%s", output)
	}

	for _, height := range []int{24, 8} {
		if err := pty.Setsize(session.tty, &pty.Winsize{Cols: 48, Rows: uint16(height)}); err != nil {
			t.Fatalf("resize pty: %v", err)
		}
		// The resize ioctl delivers SIGWINCH to the foreground process only;
		// here the form runs in this test's process group.
		if err := syscall.Kill(syscall.Getpid(), syscall.SIGWINCH); err != nil {
			t.Fatalf("notify resize: %v", err)
		}
		output = session.waitFor("mcp-11")
		if !multiSelectCursorOnOption(output, "mcp-11") || !multiSelectSelectionOnOption(output, "mcp-11") {
			t.Fatalf("PTY resize to %d rows lost the cursor or selection:\n%s", height, output)
		}
	}
	session.run("")
}

type multiSelectPTYSession struct {
	t          *testing.T
	ptmx       *os.File
	tty        *os.File
	mu         sync.Mutex
	output     strings.Builder
	waited     int
	readerDone chan struct{}
	runDone    chan error
	closed     bool
}

func newMultiSelectPTYSession(t *testing.T, form *huh.Form, width, height int) *multiSelectPTYSession {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	if err := pty.Setsize(tty, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)}); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		t.Fatalf("size pty: %v", err)
	}

	originalStdin, originalStdout, originalRun := os.Stdin, os.Stdout, huhRun
	os.Stdin, os.Stdout = tty, tty
	s := &multiSelectPTYSession{t: t, ptmx: ptmx, tty: tty, readerDone: make(chan struct{}), runDone: make(chan error, 1)}
	t.Cleanup(func() {
		s.close()
		os.Stdin, os.Stdout, huhRun = originalStdin, originalStdout, originalRun
	})
	if _, err := term.MakeRaw(int(ptmx.Fd())); err != nil {
		t.Fatalf("disable pty echo: %v", err)
	}
	go s.read()
	go func() { s.runDone <- huhRun(form) }()
	return s
}

func (s *multiSelectPTYSession) read() {
	defer close(s.readerDone)
	buf := make([]byte, 4096)
	for {
		_ = s.ptmx.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.output.Write(buf[:n])
			s.mu.Unlock()
		}
		if err != nil && !os.IsTimeout(err) {
			return
		}
	}
}

func (s *multiSelectPTYSession) waitFor(fragment string) string {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		view := ansi.Strip(s.output.String()[s.waited:])
		s.waited = s.output.Len()
		s.mu.Unlock()
		if strings.Contains(view, fragment) {
			return view
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("PTY did not render %q:\n%s", fragment, view)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *multiSelectPTYSession) run(keys string) {
	s.t.Helper()
	if keys != "" {
		if _, err := s.ptmx.WriteString(keys); err != nil {
			s.t.Fatalf("write keys to pty: %v", err)
		}
	}
	if _, err := s.ptmx.WriteString("\r"); err != nil {
		s.t.Fatalf("submit form: %v", err)
	}
	select {
	case err := <-s.runDone:
		if err != nil {
			s.t.Fatalf("run form on pty: %v", err)
		}
	case <-time.After(5 * time.Second):
		s.t.Fatal("form did not finish after keyboard input")
	}
	s.close()
}

func (s *multiSelectPTYSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.tty.Close()
	_ = s.ptmx.Close()
	select {
	case <-s.readerDone:
	case <-time.After(2 * time.Second):
		s.t.Error("PTY reader did not finish")
	}
}
