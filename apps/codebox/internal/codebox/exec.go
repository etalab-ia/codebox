package codebox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ExecResult is the outcome of a host command. A nonzero ExitCode is a normal
// result (predicates such as pgrep rely on it); Err is set only when the
// command could not be launched at all.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Runner executes host-side commands (the `tart` and `msb` CLIs).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (ExecResult, error)
	// RunEnv runs a command with extra environment variables appended to the
	// inherited environment.
	RunEnv(ctx context.Context, env []string, name string, args ...string) (ExecResult, error)
	// RunStdin runs a command in the foreground with a payload on stdin. It is
	// the synchronous counterpart of Starter.Start for one-shot guest commands
	// that must complete before the next step (secrets pushes).
	RunStdin(ctx context.Context, stdin io.Reader, name string, args ...string) (ExecResult, error)
}

// Starter launches a detached, long-running process (tart run / tart exec -i)
// with optional stdin and its output appended to a log file. It returns once
// the process has started, leaving it to outlive the CLI.
type Starter interface {
	Start(stdin io.Reader, logPath string, name string, args ...string) error
}

// OSRunner is the real Runner, backed by os/exec.
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args ...string) (ExecResult, error) {
	return osRun(ctx, nil, name, args...)
}

func (OSRunner) RunEnv(ctx context.Context, env []string, name string, args ...string) (ExecResult, error) {
	return osRun(ctx, env, name, args...)
}

func (OSRunner) RunStdin(ctx context.Context, stdin io.Reader, name string, args ...string) (ExecResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	res := ExecResult{Stdout: string(out), Stderr: string(out)}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
			return res, nil
		}
		return res, err
	}
	return res, nil
}

func osRun(ctx context.Context, env []string, name string, args ...string) (ExecResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = withEnv(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	res := ExecResult{Stdout: string(out), Stderr: string(out)}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
			return res, nil
		}
		return res, err
	}
	return res, nil
}

// maxStdinPayload bounds the detached child's stdin payload. os/exec only
// passes a descriptor straight through when Stdin is an *os.File; anything else
// gets a copying goroutine, which a detached child can outlive. Writing
// synchronously into a pipe avoids that, and keeping the payload well under any
// platform pipe buffer guarantees the write cannot block before the child
// starts. Passwords and API keys are far smaller than this.
const maxStdinPayload = 4096

// OSStarter is the real Starter. It detaches the child (new session) so the
// long-running process survives the CLI exiting, matching `nohup ... &`.
type OSStarter struct{}

func (OSStarter) Start(stdin io.Reader, logPath string, name string, args ...string) error {
	cmd := exec.Command(name, args...)

	var stdinPipe *os.File
	if stdin != nil {
		payload, err := io.ReadAll(io.LimitReader(stdin, maxStdinPayload+1))
		if err != nil {
			return err
		}
		if len(payload) > maxStdinPayload {
			return fmt.Errorf("stdin payload exceeds %d bytes", maxStdinPayload)
		}
		pr, pw, err := os.Pipe()
		if err != nil {
			return err
		}
		if _, err := pw.Write(payload); err != nil {
			pw.Close()
			pr.Close()
			return err
		}
		pw.Close() // the child sees EOF once it drains the buffer
		stdinPipe = pr
		cmd.Stdin = pr
	}

	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		if stdinPipe != nil {
			stdinPipe.Close()
		}
		return err
	}
	cmd.Stdout = log
	cmd.Stderr = log
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		if stdinPipe != nil {
			stdinPipe.Close()
		}
		log.Close()
		return err
	}
	if stdinPipe != nil {
		stdinPipe.Close() // the child keeps its own copy of the descriptor
	}
	log.Close() // the child keeps its own copy of the descriptor
	return cmd.Process.Release()
}

// runOK runs a command and treats a nonzero exit as an error. It is used for
// lifecycle commands where success is required (msb run, tart exec).
func runOK(r Runner, ctx context.Context, name string, args ...string) error {
	res, err := r.Run(ctx, name, args...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s %s failed (exit %d)", name, strings.Join(args, " "), res.ExitCode)
	}
	return nil
}

// runEnv is Run with additional environment variables.
func runEnv(r Runner, ctx context.Context, env []string, name string, args ...string) (ExecResult, error) {
	return r.RunEnv(ctx, env, name, args...)
}

// runStdinOK runs a command with a stdin payload and treats a nonzero exit
// as an error. It is the synchronous counterpart of runOK for one-shot guest
// commands (secrets pushes) that must complete before the next step.
func runStdinOK(r Runner, ctx context.Context, stdin io.Reader, name string, args ...string) error {
	res, err := r.RunStdin(ctx, stdin, name, args...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s %s failed (exit %d)", name, strings.Join(args, " "), res.ExitCode)
	}
	return nil
}

// runEnvOK is runOK with additional environment variables.
func runEnvOK(r Runner, ctx context.Context, env []string, name string, args ...string) error {
	res, err := r.RunEnv(ctx, env, name, args...)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("%s %s failed (exit %d)", name, strings.Join(args, " "), res.ExitCode)
	}
	return nil
}

// RunInteractive runs a command attached to the current terminal's stdio. It is
// used for `logs --follow` and interactive `shell`/`attach` commands.
func RunInteractive(name string, args ...string) error {
	return RunInteractiveEnv(nil, name, args...)
}

// RunInteractiveEnv is RunInteractive with additional environment variables.
func RunInteractiveEnv(env []string, name string, args ...string) error {
	cmd := interactiveCommand(name, args...)
	if len(env) > 0 {
		cmd.Env = withEnv(os.Environ(), env...)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// interactiveCommand builds the *exec.Cmd for an interactive command. On
// Windows, when the resolved program is a batch shim (.cmd/.bat, as produced by
// `npm install -g @opencode/cli`), it is invoked through the command processor
// (cmd.exe /c) because CreateProcess cannot execute a batch file directly.
func interactiveCommand(name string, args ...string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		if resolved, err := exec.LookPath(name); err == nil && isBatchFile(resolved) {
			return exec.Command("cmd.exe", append([]string{"/c", resolved}, args...)...)
		}
	}
	return exec.Command(name, args...)
}

// ValidateOpenCodeBackend rejects v1 auth settings that v2 cannot honour.
// It is intentionally start-only: cleanup and inspection must remain usable.
func ValidateOpenCodeBackend(cfg Config) error {
	if cfg.Isolation == IsolationFull {
		return nil
	}
	if cfg.Username != "" && cfg.Username != DefaultUsername {
		return fmt.Errorf("OpenCode v2 requires OPENCODE_SERVER_USERNAME=opencode; custom backend usernames are no longer supported")
	}
	if cfg.Password == "" {
		return fmt.Errorf("OpenCode v2 requires a non-empty OPENCODE_SERVER_PASSWORD; unauthenticated backend mode is no longer supported")
	}
	return nil
}

// OpenCodeClientEnv carries the v2 credential only in the child environment,
// overriding inherited OPENCODE_PASSWORD (which has precedence over the old name).
func OpenCodeClientEnv(password string) []string {
	return []string{OpenCodeDisableUpdateAssignment, "OPENCODE_PASSWORD=" + password}
}

// OpenCodeHelp captures the installed OpenCode CLI's help using the same
// Windows batch-shim handling as interactiveCommand.
func OpenCodeHelp(ctx context.Context, args ...string) (string, error) {
	prepared := interactiveCommand("opencode", args...)
	cmd := exec.CommandContext(ctx, prepared.Path, prepared.Args[1:]...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// OpenCodeHasSessions parses `opencode session list --format json`. The CLI
// prints no output when the current directory has no sessions.
func OpenCodeHasSessions(output string) (bool, error) {
	if strings.TrimSpace(output) == "" {
		return false, nil
	}
	var sessions []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(output), &sessions); err != nil {
		return false, fmt.Errorf("parse OpenCode session list: %w", err)
	}
	for _, session := range sessions {
		if !strings.HasPrefix(session.ID, "ses") {
			return false, fmt.Errorf("OpenCode session list contains an invalid session ID")
		}
	}
	return len(sessions) > 0, nil
}

// OpenCodeRemoteHasSessions checks the running backend server's session API;
// the instance context scopes the list to that project's sessions.
func OpenCodeRemoteHasSessions(ctx context.Context, endpoint, username, password string) (bool, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	var location struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := FetchJSON(ctx, client, endpoint, username, password, "/api/location", &location); err != nil {
		return false, fmt.Errorf("resolve remote OpenCode project: %w", err)
	}
	if location.Project.ID == "" {
		return false, fmt.Errorf("remote OpenCode location has no project ID")
	}
	target, err := url.JoinPath(endpoint, "api/session")
	if err != nil {
		return false, fmt.Errorf("build OpenCode session URL: %w", err)
	}
	query := url.Values{"project": {location.Project.ID}, "parentID": {"null"}, "limit": {"1"}, "order": {"desc"}}
	target += "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, fmt.Errorf("create OpenCode session request: %w", err)
	}
	req.SetBasicAuth(username, password)
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("list remote OpenCode sessions: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("list remote OpenCode sessions: HTTP %d", resp.StatusCode)
	}
	const maxSessionListBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSessionListBytes+1))
	if err != nil {
		return false, fmt.Errorf("read remote OpenCode sessions: %w", err)
	}
	if len(body) > maxSessionListBytes {
		return false, fmt.Errorf("remote OpenCode session list exceeds %d bytes", maxSessionListBytes)
	}
	var page struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &page); err != nil || len(page.Data) == 0 || string(page.Data) == "null" {
		return false, fmt.Errorf("invalid OpenCode v2 session page")
	}
	return OpenCodeHasSessions(string(page.Data))
}

// OpenCodeContinueArgs returns the session-resume flag only when the
// installed CLI advertises it. Older OpenCode builds still launch normally;
// the caller can warn that auto-resume is unavailable.
func OpenCodeContinueArgs(help string) []string {
	if strings.Contains(help, "--continue") {
		return []string{"--continue"}
	}
	return nil
}

// OpenCodeResumeArgs avoids --continue on first launch: OpenCode 1.18.30's
// empty-session path can fail before the TUI creates a session. Once a session
// exists, pass the flag only when the installed CLI advertises it.
func OpenCodeResumeArgs(hasSessions bool, sessionErr error, help string, helpErr error) ([]string, bool) {
	if sessionErr != nil {
		return nil, true
	}
	if !hasSessions {
		return nil, false
	}
	if helpErr != nil {
		return nil, true
	}
	args := OpenCodeContinueArgs(help)
	return args, len(args) == 0
}

// WarnOpenCodeResumeUnavailable explains that the launch will start fresh
// because the existing session could not be safely resumed.
func WarnOpenCodeResumeUnavailable(target string) {
	fmt.Fprintf(os.Stderr, "Avertissement : impossible de vérifier la session OpenCode %s ; le TUI démarrera une nouvelle session. Vérifiez le runtime ou le CLI pour rétablir la reprise automatique.\n", target)
}

// isBatchFile reports whether path names a Windows batch file.
func isBatchFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".cmd" || ext == ".bat"
}

// withEnv returns base with the given KEY=VALUE pairs applied, replacing any
// existing entry for the same key. Appending instead would leave duplicates,
// and env lookup returns the first match, so the inherited value would win.
func withEnv(base []string, kv ...string) []string {
	replaced := make(map[string]bool, len(kv))
	for _, pair := range kv {
		if i := strings.IndexByte(pair, '='); i >= 0 {
			replaced[pair[:i]] = true
		}
	}
	out := make([]string, 0, len(base)+len(kv))
	for _, entry := range base {
		key := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			key = entry[:i]
		}
		if replaced[key] {
			continue
		}
		out = append(out, entry)
	}
	return append(out, kv...)
}

// commandNotFound reports whether err means the command binary is missing.
func commandNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound)
}
