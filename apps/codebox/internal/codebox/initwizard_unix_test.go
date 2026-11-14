//go:build !windows

package codebox

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestInitPlanRefusesNonRegularAgentsBeforeReading(t *testing.T) {
	// A FIFO or a symlink to an unbounded device must be refused by the
	// entry-type check, not read: ReadFile on either blocks or exhausts
	// memory.
	if _, err := parseRuntimeName(string(RuntimeTart)); err != nil {
		t.Skipf("%s is not selectable on this platform (%v)", RuntimeTart, err)
	}
	cases := []struct {
		name  string
		entry func(t *testing.T, root string)
	}{
		{
			name: "FIFO",
			entry: func(t *testing.T, root string) {
				if err := syscall.Mkfifo(filepath.Join(root, "AGENTS.md"), 0o644); err != nil {
					t.Skipf("cannot create FIFO: %v", err)
				}
			},
		},
		{
			name: "symlink to unbounded device",
			entry: func(t *testing.T, root string) {
				if _, err := os.Stat("/dev/zero"); err != nil {
					t.Skipf("no /dev/zero on this platform: %v", err)
				}
				if err := os.Symlink("/dev/zero", filepath.Join(root, "AGENTS.md")); err != nil {
					t.Skipf("cannot create symlink: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := initTestRoot(t)
			manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
			if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{
				Runtime: string(RuntimeTart), Isolation: string(IsolationFull),
			}); err != nil {
				t.Fatal(err)
			}
			if err := WriteLockfile(DefaultFS, lockPath, Lockfile{Entries: map[string]string{}}); err != nil {
				t.Fatal(err)
			}
			tc.entry(t, root)
			wizard := InitWizard{StateDir: t.TempDir()}
			_, err := wizard.Plan(InitAnswers{Root: root, Runtime: RuntimeTart})
			if err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("plan must refuse a non-regular AGENTS.md before reading it, got %v", err)
			}
		})
	}
}

func TestInitApplyRefusesNonRegularAgentsAddedAfterPlanning(t *testing.T) {
	// The apply-time recheck must also refuse a non-regular entry that
	// appeared after planning instead of reading it.
	if _, err := parseRuntimeName(string(RuntimeTart)); err != nil {
		t.Skipf("%s is not selectable on this platform (%v)", RuntimeTart, err)
	}
	root := initTestRoot(t)
	manifestPath, lockPath := ProjectManifestPath(root), ProjectLockPath(root)
	if err := WriteProjectManifest(DefaultFS, manifestPath, ProjectManifest{
		Runtime: string(RuntimeTart), Isolation: string(IsolationFull),
	}); err != nil {
		t.Fatal(err)
	}
	if err := WriteLockfile(DefaultFS, lockPath, Lockfile{Entries: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	wizard := InitWizard{StateDir: t.TempDir()}
	plan, err := wizard.Plan(InitAnswers{Root: root, Runtime: RuntimeTart, SkillsLocalOnly: true, SkillsLocalOnlySet: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.InstructionsPath != "" {
		t.Fatal("a plan without AGENTS.md must not claim it")
	}
	if err := syscall.Mkfifo(filepath.Join(root, "AGENTS.md"), 0o644); err != nil {
		t.Skipf("cannot create FIFO: %v", err)
	}
	if err := wizard.Apply(plan, true); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("apply must refuse a FIFO AGENTS.md before reading it, got %v", err)
	}
}
