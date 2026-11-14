//go:build windows

package codebox

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// diskFree reports the free bytes of the filesystem containing path, via
// PowerShell's Get-PSDrive. The wincred adapter established the repo's
// Windows pattern (PowerShell for system surfaces), and this keeps one FFI
// style instead of introducing raw syscall plumbing.
func diskFree(path string) (int64, error) {
	// Bounded like the other probes: a hung PowerShell must not hang the
	// preflight.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := OSRunner{}.Run(ctx, "powershell", "-NoProfile", "-NonInteractive",
		"-Command", "(Get-PSDrive (Get-Item -LiteralPath '"+escapePowerShellString(path)+"').PSDrive.Name.ToString()).Free")
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("disk free probe failed (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	v, err := strconv.ParseInt(strings.TrimSpace(res.Stdout), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse disk free output: %w", err)
	}
	return v, nil
}

// escapePowerShellString doubles single quotes: the value is already inside
// a single-quoted PowerShell string literal.
func escapePowerShellString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
