package main

import (
	"context"
	"fmt"
	"io"

	"github.com/etalab-ia/codebox/apps/codebox/internal/codebox"
)

type acpRuntime interface {
	codebox.Backend
	RunACP(ctx context.Context, stdin io.ReadCloser, stdout, stderr io.Writer) error
}

// acpCmd starts the prepared guest and exposes only the policy-mediated ACP
// stream. Setup output is redirected to stderr by run before reaching here.
func acpCmd(d *codebox.Dispatcher, cfg codebox.Config, rt codebox.Runtime, input io.ReadCloser, output, diagnostics io.Writer) (int, error) {
	if rt != codebox.RuntimeMicrosandbox {
		return 2, fmt.Errorf("ACP nécessite le runtime microsandbox")
	}
	if cfg.Isolation != codebox.IsolationFull {
		return 2, fmt.Errorf("ACP nécessite l'isolation full")
	}
	if cfg.EffectiveHarness() != codebox.HarnessOpenCode {
		return 2, fmt.Errorf("ACP nécessite le harnais OpenCode")
	}
	backend, err := d.Backend(rt)
	if err != nil {
		return 1, err
	}
	runtime, ok := backend.(acpRuntime)
	if !ok {
		return 1, fmt.Errorf("le runtime %s ne fournit pas de transport ACP", rt)
	}
	ctx := context.Background()
	if err := d.Prepare(ctx, rt); err != nil {
		return 1, err
	}
	if err := startWithLaunchReport(ctx, d, backend); err != nil {
		return 1, err
	}
	err = runtime.RunACP(ctx, codebox.NewACPPolicyReader(input), output, diagnostics)
	return exitCodeOf(err), err
}
