package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// readPassword reads a line from the terminal with echo disabled. It is
// the masked interactive input for `codebox auth add`. On a terminal
// that cannot be configured it still reads the line; the caller's prompt
// already told the user the hiding is best-effort.
func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		value, err := term.ReadPassword(fd)
		if err != nil {
			return "", fmt.Errorf("read secret: %w", err)
		}
		return string(value), nil
	}
	// Not a terminal: read a plain line. The --stdin flag is the
	// documented noninteractive path, so arriving here without it means
	// stdin was redirected; treat it the same way.
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read secret from stdin: %w", err)
	}
	return strings.TrimSpace(line), nil
}
