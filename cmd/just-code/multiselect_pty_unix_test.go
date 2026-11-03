//go:build !windows && !race

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/huh/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// These scenarios use a real PTY and its SIGWINCH delivery. Bubble Tea 2.1.x
// has a race between listenForResize and its initial size request in
// checkResize (tty.go), so race-enabled builds cover the direct-render
// regressions in multiselect_render_test.go instead.
const multiSelectPTYScenarioEnv = "JUST_CODE_TEST_MULTISELECT_PTY"

func TestMultiSelectFormsRenderOptionsOnAPTY(t *testing.T) {
	for _, tc := range multiSelectRenderCases() {
		t.Run(tc.name, func(t *testing.T) {
			scenario := map[string]string{
				"setup platforms":        "setup",
				"init MCPs":              "mcp",
				"workspace apply detail": "apply",
			}[tc.name]
			for _, height := range []int{8, 24} {
				session := newMultiSelectPTYSession(t, scenario, 100, height)
				output := session.waitFor(tc.options[0])
				for _, option := range tc.options {
					if !strings.Contains(output, option) {
						t.Errorf("PTY height %d hides option %q:\n%s", height, option, output)
					}
				}
				if !multiSelectCursorOnOption(output, tc.options[0]) {
					t.Errorf("PTY height %d hides the option cursor:\n%s", height, output)
				}
				session.submit()
			}
		})
	}
}

func TestMultiSelectPTYResizePreservesCursorAndSelection(t *testing.T) {
	session := newMultiSelectPTYSession(t, "resize", 48, 8)
	_ = session.waitFor("mcp-00")
	session.write(strings.Repeat("\x1b[B", 11) + " ")
	output := session.waitFor("> ✓ mcp-11")
	if !multiSelectCursorOnOption(output, "mcp-11") || !multiSelectSelectionOnOption(output, "mcp-11") {
		t.Fatalf("PTY scrolling or selection lost the last option:\n%s", output)
	}
	for _, height := range []int{24, 8} {
		mark := session.outputPosition()
		session.resize(48, height)
		output = session.waitFrom(mark, "> ✓ mcp-11")
		if !multiSelectCursorOnOption(output, "mcp-11") || !multiSelectSelectionOnOption(output, "mcp-11") {
			t.Fatalf("PTY resize to %d rows lost the cursor or selection:\n%s", height, output)
		}
	}
	session.submit()
}

func TestMultiSelectPTYShrinkPreservesNonFinalCursorAndSelection(t *testing.T) {
	session := newMultiSelectPTYSession(t, "resize", 100, 24)
	_ = session.waitFor("mcp-00")
	session.write(strings.Repeat("\x1b[B", 4) + " ")
	output := session.waitFor("✓ mcp-04")
	if !multiSelectSelectionOnOption(output, "mcp-04") {
		t.Fatalf("PTY setup did not select mcp-04:\n%s", output)
	}
	mark := session.outputPosition()
	session.resize(48, 8)
	output = session.waitFrom(mark, "> ✓ mcp-04")
	if !multiSelectCursorOnOption(output, "mcp-04") || !multiSelectSelectionOnOption(output, "mcp-04") {
		t.Fatalf("PTY shrink moved or hid the cursor:\n%s", output)
	}
	session.submit()
}

func TestMultiSelectPTYResizePreservesActiveFilter(t *testing.T) {
	session := newMultiSelectPTYSession(t, "filter", 80, 24)
	_ = session.waitFor("data.gouv.fr")
	mark := session.outputPosition()
	session.write("/data")
	_ = session.waitFrom(mark, "set filter")
	mark = session.outputPosition()
	session.resize(80, 23)
	output := session.waitFrom(mark, "> • data.gouv.fr")
	if !multiSelectCursorOnOption(output, "data.gouv.fr") || strings.Contains(output, "/dataG") {
		t.Fatalf("PTY resize lost the active filter's option:\n%s", output)
	}
	// The first Enter commits the active filter; the second submits the form.
	session.write("\r")
	session.submit()
}

// TestMultiSelectPTYChild is re-executed with its own controlling terminal.
// Keeping Bubble Tea in a subprocess avoids sharing terminal descriptors or
// package-level I/O with the parent test's PTY reader.
func TestMultiSelectPTYChild(t *testing.T) {
	scenario := os.Getenv(multiSelectPTYScenarioEnv)
	if scenario == "" {
		t.Skip("only runs as a PTY subprocess")
	}
	t.Setenv("TERM", "xterm-256color")
	var form *huh.Form
	switch scenario {
	case "setup":
		form = captureSetupPlatformsForm(t)
	case "mcp":
		form = captureInitMCPsForm(t)
	case "apply":
		form = captureWorkspaceApplyDetailForm(t)
	case "resize":
		options := make([]huh.Option[string], 12)
		for i := range options {
			label := fmt.Sprintf("mcp-%02d", i)
			options[i] = huh.NewOption(label, label)
		}
		selected := []string{}
		form = huh.NewForm(huh.NewGroup(newProjectMCPsMultiSelect(options, &selected)))
	case "filter":
		form = captureInitMCPsForm(t)
	default:
		t.Fatalf("unknown PTY scenario %q", scenario)
	}
	if err := huhRun(form); err != nil {
		t.Fatalf("run PTY form: %v", err)
	}
}

type multiSelectPTYSession struct {
	t          *testing.T
	cmd        *exec.Cmd
	ptmx       *os.File
	mu         sync.Mutex
	output     strings.Builder
	waited     int
	readerDone chan struct{}
	done       chan struct{}
	runErr     error
	closed     bool
}

func newMultiSelectPTYSession(t *testing.T, scenario string, width, height int) *multiSelectPTYSession {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMultiSelectPTYChild$", "-test.count=1", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), multiSelectPTYScenarioEnv+"="+scenario, "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	s := &multiSelectPTYSession{t: t, cmd: cmd, ptmx: ptmx, readerDone: make(chan struct{}), done: make(chan struct{})}
	t.Cleanup(s.close)
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)}); err != nil {
		t.Fatalf("size pty: %v", err)
	}
	go s.read()
	go func() {
		s.runErr = cmd.Wait()
		close(s.done)
	}()
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
			s.mu.Lock()
			all := ansi.Strip(s.output.String())
			s.mu.Unlock()
			s.t.Fatalf("PTY did not render %q:\n%s", fragment, all)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *multiSelectPTYSession) outputPosition() int {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output.Len()
}

func (s *multiSelectPTYSession) waitFrom(mark int, fragment string) string {
	s.t.Helper()
	s.mu.Lock()
	s.waited = mark
	s.mu.Unlock()
	return s.waitFor(fragment)
}

func (s *multiSelectPTYSession) write(keys string) {
	s.t.Helper()
	if _, err := s.ptmx.WriteString(keys); err != nil {
		s.t.Fatalf("write keys to pty: %v", err)
	}
}

func (s *multiSelectPTYSession) resize(width, height int) {
	s.t.Helper()
	// Setsize on the master delivers SIGWINCH to the child foreground process.
	if err := pty.Setsize(s.ptmx, &pty.Winsize{Cols: uint16(width), Rows: uint16(height)}); err != nil {
		s.t.Fatalf("resize pty: %v", err)
	}
}

func (s *multiSelectPTYSession) submit() {
	s.t.Helper()
	s.write("\r")
	select {
	case <-s.done:
		if s.runErr != nil {
			s.mu.Lock()
			output := ansi.Strip(s.output.String())
			s.mu.Unlock()
			s.t.Fatalf("run form on pty: %v\n%s", s.runErr, output)
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
	if s.cmd.Process != nil {
		select {
		case <-s.done:
		default:
			_ = s.cmd.Process.Kill()
			<-s.done
		}
	}
	_ = s.ptmx.Close()
	select {
	case <-s.readerDone:
	case <-time.After(2 * time.Second):
		s.t.Error("PTY reader did not finish")
	}
}
