package codebox

import (
	"context"
	"fmt"
	"strings"
)

// templateValidateSupported reports whether this limactl can resolve a
// template locator before using it.
//
// Lima 1.0.x has no `template` subcommand: `limactl validate` there treats its
// argument as a filesystem path, so it rejects every locator form that
// `limactl create` resolves happily (`template://debian-12`, an https URL).
// Probing the subcommand's help answers the question the caller actually has —
// "can this limactl check the image?" — without parsing a version string that
// would need updating for every future release.
func templateValidateSupported(ctx context.Context, r Runner) (bool, error) {
	res, err := r.Run(ctx, "limactl", "template", "validate", "--help")
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// ValidateBaseTemplateImage reports whether `limactl create` can resolve the
// configured AGENT_VM_IMAGE.
//
// An image that Lima cannot resolve fails the build several minutes in, after
// the previous template has already been deleted, with output that runOK
// discarded. Checking it first turns that into one line naming the variable,
// the value, and Lima's own diagnostic.
//
// The check is best-effort by design: when this limactl cannot validate
// locators, the image is not rejected for it. Refusing to build on a Lima that
// simply lacks the subcommand would break every 1.0.x install over a check
// that did not exist there.
func ValidateBaseTemplateImage(ctx context.Context, r Runner, image string) error {
	if strings.TrimSpace(image) == "" {
		return fmt.Errorf("AGENT_VM_IMAGE is empty; set it to a Lima image (for example %s)", DefaultAgentVMImage)
	}
	supported, err := templateValidateSupported(ctx, r)
	if err != nil {
		return err
	}
	if !supported {
		return nil
	}
	res, err := r.Run(ctx, "limactl", "template", "validate", image)
	if err != nil {
		return err
	}
	if res.ExitCode == 0 {
		return nil
	}
	// A failed check does not yet say the image is wrong: limactl refuses to
	// run at all for some callers (`must not run as the root user`), and that
	// refusal arrives on this same command. In Lima 1.x that gate sits in a
	// PersistentPreRunE on the root command, so it applies to every subcommand
	// while --help bypasses it — which is why the capability probe above
	// passes as root. A subcommand that carries no image therefore fails
	// identically and serves as a witness.
	//
	// Lima 2.x moved the same gate to a panic at the top of main(), which
	// --help does not bypass, so as root the probe exits non-zero and this
	// check reports "unsupported" instead. That path skips validation rather
	// than witnessing it, and is harmless: as root on 2.x every limactl call
	// panics, so the build fails on its own with Lima's message before this
	// check could matter.
	//
	// `info` is that subcommand: it reads the template store, `limayaml` and
	// LimaHome — the same inputs `template validate` needs — so it fails when
	// they are unavailable, and it touches no instance state, so a leftover
	// instance cannot make it fail. Probing the default image instead would
	// leave this undiagnosed whenever the configured image IS the default,
	// which is the common case and the one that reported the bug.
	//
	// A failing witness is inconclusive rather than proof of a shared cause,
	// so neither failure is attributed to AGENT_VM_IMAGE and both diagnostics
	// are reported. The witness can indeed fail for a reason that has nothing
	// to do with the configured image — `info` reads the template store,
	// `default.yaml` and LimaHome, so a broken store fails it while a
	// genuinely bad image also fails the validation. Nothing cheap separates
	// those two: Lima exits non-zero in both cases, and matching the refusal text
	// would tie the attribution to Lima's wording, which is exactly the
	// fragility the witness exists to avoid. The result therefore reports
	// that no verdict is available rather than asserting either cause.
	// Only paid on failure.
	witness, witnessErr := r.Run(ctx, "limactl", "info")
	if witnessErr != nil {
		return witnessErr
	}
	if witness.ExitCode != 0 {
		return fmt.Errorf("cannot validate AGENT_VM_IMAGE=%q: limactl failed its own diagnostic too (exit %d): %s; the image check reported: %s",
			image, witness.ExitCode, commandDiagnostic(witness), commandDiagnostic(res))
	}
	// The remedy has to make sense for the value that failed. Telling someone
	// to set the variable to the built-in default when it is already the
	// default sends them in a circle; the useful fact is that this limactl's
	// own template store is incomplete.
	remedy := fmt.Sprintf("set AGENT_VM_IMAGE to a resolvable image (for example %s)", DefaultAgentVMImage)
	if image == DefaultAgentVMImage {
		remedy = "this is the built-in default, so this limactl's template store is incomplete; set AGENT_VM_IMAGE to another resolvable image, or repair the Lima template store"
	}
	return fmt.Errorf("AGENT_VM_IMAGE=%q is not a Lima image this limactl can resolve: %s; %s",
		image, commandDiagnostic(res), remedy)
}

// commandDiagnostic returns the most useful explanation a failed command left
// behind, or a neutral phrase when it left none.
func commandDiagnostic(res ExecResult) string {
	for _, candidate := range []string{res.Stderr, res.Stdout} {
		if detail := strings.TrimSpace(candidate); detail != "" {
			return detail
		}
	}
	return fmt.Sprintf("no diagnostic output (exit %d)", res.ExitCode)
}
