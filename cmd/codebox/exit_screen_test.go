package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/etalab-ia/codebox/internal/codebox"
)

// The exit screen (issue #174) and the apply flow (issue #173) are terminal
// flows: their interactive branches are gated on a terminal probe that a test
// cannot satisfy with a pipe. The probe is a var seam, the same pattern as
// stdinIsTTYFn, so these tests pin both branches of the contract.

// forceExitTTY pins the exit screen's terminal probe for the test.
func forceExitTTY(t *testing.T, tty bool) {
	t.Helper()
	orig := exitIsTTYFn
	exitIsTTYFn = func() bool { return tty }
	t.Cleanup(func() { exitIsTTYFn = orig })
}

// forceExitStopReply answers the (o/N) prompt with a fixed reply.
func forceExitStopReply(t *testing.T, reply string) {
	t.Helper()
	orig := exitStopPromptFn
	exitStopPromptFn = func() string { return reply }
	t.Cleanup(func() { exitStopPromptFn = orig })
}

// exitScreenBackend is the Backend double for the exit screen: the screen
// probes IsRunning and, only on an explicit stop answer, calls Stop.
type exitScreenBackend struct {
	codebox.Backend
	running bool
	stopped bool
	stopErr error
}

func (b *exitScreenBackend) IsRunning(context.Context) (bool, error) { return b.running, nil }

func (b *exitScreenBackend) Stop(context.Context) error {
	if b.stopErr != nil {
		return b.stopErr
	}
	b.stopped = true
	return nil
}

func TestFormatMemoryMB(t *testing.T) {
	cases := []struct {
		mb   int
		want string
	}{
		{4096, "4 Go"},
		{2048, "2 Go"},
		{1024, "1 Go"},
		{2560, "2,5 Go"},
		{1000, "0,9 Go"},
		{1029, "1 Go"}, // the fraction rounds away
		{0, ""},
		{-512, ""},
	}
	for _, tc := range cases {
		if got := formatMemoryMB(tc.mb); got != tc.want {
			t.Errorf("formatMemoryMB(%d) = %q, want %q", tc.mb, got, tc.want)
		}
	}
}

func TestReservedMemoryPhrase(t *testing.T) {
	cases := []struct {
		name string
		cfg  codebox.Config
		rt   codebox.Runtime
		want string
	}{
		{"microsandbox sizes from MemoryMB", codebox.Config{MemoryMB: 4096}, codebox.RuntimeMicrosandbox, "4 Go"},
		{"agent-vm sizes from its gigabytes", codebox.Config{AgentVMMemoryGB: 8}, codebox.RuntimeAgentVM, "8 Go"},
		{"tart reports none", codebox.Config{}, codebox.RuntimeTart, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reservedMemoryPhrase(tc.cfg, tc.rt); got != tc.want {
				t.Errorf("reservedMemoryPhrase = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResumeDurationPhrase(t *testing.T) {
	store := codebox.NewLaunchTimingStore(codebox.DefaultFS, t.TempDir())
	if err := store.Observe("single", codebox.LaunchWarm, 25*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe("range", codebox.LaunchWarm, 40*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe("range", codebox.LaunchWarm, 20*time.Second); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		rt     codebox.Runtime
		launch *codebox.LaunchUX
		want   string
	}{
		{"tart announces nothing", codebox.RuntimeTart, nil, ""},
		{"nil launch falls back", codebox.RuntimeMicrosandbox, nil, "environ 30 secondes"},
		{"empty key falls back", codebox.RuntimeMicrosandbox, &codebox.LaunchUX{TimingStore: store}, "environ 30 secondes"},
		{"nil store falls back", codebox.RuntimeMicrosandbox, &codebox.LaunchUX{TimingKey: "k"}, "environ 30 secondes"},
		{"unmeasured key falls back", codebox.RuntimeMicrosandbox, &codebox.LaunchUX{TimingKey: "unmeasured", TimingStore: store}, "environ 30 secondes"},
		{"single measurement", codebox.RuntimeMicrosandbox, &codebox.LaunchUX{TimingKey: "single", TimingStore: store}, "25 secondes"},
		{"measured range", codebox.RuntimeMicrosandbox, &codebox.LaunchUX{TimingKey: "range", TimingStore: store}, "20 secondes à 40 secondes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resumeDurationPhrase(tc.rt, tc.launch); got != tc.want {
				t.Errorf("resumeDurationPhrase = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGuestChangeMarker(t *testing.T) {
	cases := []struct {
		status byte
		want   string
	}{
		{'A', "+"},
		{'D', "-"},
		{'M', "~"},
		{'?', "~"},
	}
	for _, tc := range cases {
		if got := guestChangeMarker(tc.status); got != tc.want {
			t.Errorf("guestChangeMarker(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestFormatApplyList(t *testing.T) {
	two := []codebox.GuestChange{
		{Rel: "src/app.js", Status: 'M', Additions: 12, Deletions: 3},
		{Rel: "README.md", Status: 'A', Additions: 84, Deletions: 0},
	}
	if got, want := formatApplyList(two, nil, nil),
		"Modifications de l'agent dans la machine isolée : 2 fichiers\n\n"+
			"  ~ src/app.js +12 \u22123\n"+
			"  + README.md  +84 \u22120\n"; got != want {
		t.Errorf("formatApplyList =\n%q\nwant\n%q", got, want)
	}

	recovered := map[string]bool{"src/app.js": true}
	if got, want := formatApplyList(two, recovered, nil),
		"Modifications de l'agent dans la machine isolée : 2 fichiers\n\n"+
			"  ~ src/app.js +12 \u22123   (déjà récupéré)\n"+
			"  + README.md  +84 \u22120\n"; got != want {
		t.Errorf("formatApplyList(recovered) =\n%q\nwant\n%q", got, want)
	}

	refused := map[string]string{"README.md": "hors du projet"}
	if got, want := formatApplyList(two, nil, refused),
		"Modifications de l'agent dans la machine isolée : 2 fichiers\n\n"+
			"  ~ src/app.js +12 \u22123\n"+
			"  + README.md  +84 \u22120   (exclu : hors du projet)\n"; got != want {
		t.Errorf("formatApplyList(refused) =\n%q\nwant\n%q", got, want)
	}

	binary := []codebox.GuestChange{{Rel: "logo.png", Status: 'A', Binary: true}}
	if got, want := formatApplyList(binary, nil, nil),
		"Modifications de l'agent dans la machine isolée : 1 fichier\n\n"+
			"  + logo.png binaire\n"; got != want {
		t.Errorf("formatApplyList(binary) =\n%q\nwant\n%q", got, want)
	}

	synthetic := []codebox.GuestChange{{Rel: "src/app.js", Status: 'M', Synthetic: true}}
	if got, want := formatApplyList(synthetic, nil, nil),
		"Modifications de l'agent dans la machine isolée : 1 fichier\n\n"+
			"  ~ src/app.js rétabli\n"; got != want {
		t.Errorf("formatApplyList(synthetic) =\n%q\nwant\n%q", got, want)
	}
}

func TestExitStopQuestionCostLines(t *testing.T) {
	forceExitTTY(t, true)
	forceExitStopReply(t, "\n") // the default keeps the machine running

	store := codebox.NewLaunchTimingStore(codebox.DefaultFS, t.TempDir())
	if err := store.Observe("measured", codebox.LaunchWarm, 25*time.Second); err != nil {
		t.Fatal(err)
	}
	measured := codebox.NewDispatcherWith(codebox.Config{}, nil)
	measured.Launch = &codebox.LaunchUX{TimingKey: "measured", TimingStore: store}

	cases := []struct {
		name     string
		cfg      codebox.Config
		rt       codebox.Runtime
		d        *codebox.Dispatcher
		keepLine string
		stopLine string
	}{
		{
			name:     "microsandbox announces reserved memory and the resume fallback",
			cfg:      codebox.Config{MemoryMB: 4096},
			rt:       codebox.RuntimeMicrosandbox,
			d:        codebox.NewDispatcherWith(codebox.Config{}, nil),
			keepLine: "  Non (recommandé) : la reprise est immédiate ; elle garde 4 Go de mémoire réservés.\n",
			stopLine: "  Oui : la mémoire est libérée ; la prochaine reprise prend environ 30 secondes.\n",
		},
		{
			name:     "agent-vm announces its own memory and no resume delay",
			cfg:      codebox.Config{AgentVMMemoryGB: 8},
			rt:       codebox.RuntimeAgentVM,
			d:        codebox.NewDispatcherWith(codebox.Config{}, nil),
			keepLine: "  Non (recommandé) : la reprise est immédiate ; elle garde 8 Go de mémoire réservés.\n",
			stopLine: "  Oui : la mémoire est libérée.\n",
		},
		{
			name:     "tart announces neither reserved memory nor resume delay",
			cfg:      codebox.Config{},
			rt:       codebox.RuntimeTart,
			d:        codebox.NewDispatcherWith(codebox.Config{}, nil),
			keepLine: "  Non (recommandé) : la reprise est immédiate.\n",
			stopLine: "  Oui : la mémoire est libérée.\n",
		},
		{
			name:     "a measured resume replaces the fallback figure",
			cfg:      codebox.Config{MemoryMB: 4096},
			rt:       codebox.RuntimeMicrosandbox,
			d:        measured,
			keepLine: "  Non (recommandé) : la reprise est immédiate ; elle garde 4 Go de mémoire réservés.\n",
			stopLine: "  Oui : la mémoire est libérée ; la prochaine reprise prend 25 secondes.\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &exitScreenBackend{running: true}
			out := captureStdout(t, func() {
				exitStopQuestion(context.Background(), b, tc.cfg, tc.rt, tc.d)
			})
			if !strings.Contains(out, "\nArrêter la machine isolée ?\n") {
				t.Errorf("question missing from:\n%s", out)
			}
			if !strings.Contains(out, tc.keepLine) {
				t.Errorf("keep line missing from:\n%s", out)
			}
			if !strings.Contains(out, tc.stopLine) {
				t.Errorf("stop line missing from:\n%s", out)
			}
			if b.stopped {
				t.Error("the default answer stopped the machine")
			}
		})
	}
}

func TestExitScreenStopAnswerStopsTheMachine(t *testing.T) {
	forceExitTTY(t, true)
	forceExitStopReply(t, "o\n")
	b := &exitScreenBackend{running: true}
	d := codebox.NewDispatcherWith(codebox.Config{}, map[codebox.Runtime]codebox.Backend{
		codebox.RuntimeMicrosandbox: b,
	})
	out := captureStdout(t, func() {
		exitScreen(context.Background(), d, codebox.Config{MemoryMB: 4096}, codebox.RuntimeMicrosandbox, "inst")
	})
	want := "\nSession terminée.\n" +
		"\nArrêter la machine isolée ?\n" +
		"  Non (recommandé) : la reprise est immédiate ; elle garde 4 Go de mémoire réservés.\n" +
		"  Oui : la mémoire est libérée ; la prochaine reprise prend environ 30 secondes.\n" +
		"La machine isolée est arrêtée.\n" +
		"Pour reprendre cette session : codebox\n"
	if out != want {
		t.Errorf("exitScreen output =\n%q\nwant\n%q", out, want)
	}
	if !b.stopped {
		t.Error("the stop answer did not stop the machine")
	}
}

func TestExitScreenAnswers(t *testing.T) {
	forceExitTTY(t, true)
	cases := []struct {
		name    string
		reply   string
		stops   bool
		closing string
	}{
		{"empty keeps", "\n", false, "La machine isolée reste démarrée.\n"},
		{"n keeps", "n\n", false, "La machine isolée reste démarrée.\n"},
		{"N keeps", "N\n", false, "La machine isolée reste démarrée.\n"},
		{"anything else keeps", "peut-être\n", false, "La machine isolée reste démarrée.\n"},
		{"o stops", "o\n", true, "La machine isolée est arrêtée.\n"},
		{"OUI stops", " OUI \n", true, "La machine isolée est arrêtée.\n"},
		{"yes stops", "yes\n", true, "La machine isolée est arrêtée.\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forceExitStopReply(t, tc.reply)
			b := &exitScreenBackend{running: true}
			d := codebox.NewDispatcherWith(codebox.Config{}, map[codebox.Runtime]codebox.Backend{
				codebox.RuntimeMicrosandbox: b,
			})
			out := captureStdout(t, func() {
				exitScreen(context.Background(), d, codebox.Config{}, codebox.RuntimeMicrosandbox, "inst")
			})
			if b.stopped != tc.stops {
				t.Errorf("stopped = %v, want %v", b.stopped, tc.stops)
			}
			if !strings.Contains(out, tc.closing) {
				t.Errorf("closing line %q missing from:\n%s", tc.closing, out)
			}
		})
	}
}

func TestExitScreenStopFailureIsReported(t *testing.T) {
	forceExitTTY(t, true)
	forceExitStopReply(t, "o\n")
	b := &exitScreenBackend{running: true, stopErr: errors.New("la machine n'a pas répondu")}
	d := codebox.NewDispatcherWith(codebox.Config{}, map[codebox.Runtime]codebox.Backend{
		codebox.RuntimeMicrosandbox: b,
	})
	var out, errOut string
	out = captureStdout(t, func() {
		errOut = captureStderr(t, func() {
			exitScreen(context.Background(), d, codebox.Config{}, codebox.RuntimeMicrosandbox, "inst")
		})
	})
	if !strings.Contains(errOut, "L'arrêt a échoué : la machine n'a pas répondu") {
		t.Errorf("failure missing from stderr:\n%s", errOut)
	}
	if !strings.Contains(out, "Pour reprendre cette session : codebox") {
		t.Errorf("resume line missing from stdout:\n%s", out)
	}
	if strings.Contains(out, "La machine isolée est arrêtée.") || strings.Contains(out, "La machine isolée reste démarrée.") {
		t.Errorf("a stop verdict was printed despite the failure:\n%s", out)
	}
	if b.stopped {
		t.Error("the backend recorded a stop despite the failure")
	}
}

func TestExitScreenNonTTYSkipsTheQuestions(t *testing.T) {
	forceExitTTY(t, false)
	b := &exitScreenBackend{running: true}
	d := codebox.NewDispatcherWith(codebox.Config{}, map[codebox.Runtime]codebox.Backend{
		codebox.RuntimeMicrosandbox: b,
	})
	out := captureStdout(t, func() {
		exitScreen(context.Background(), d, codebox.Config{}, codebox.RuntimeMicrosandbox, "inst")
	})
	want := "\nSession terminée.\n" +
		"La machine isolée reste démarrée ; lancez 'codebox stop' pour l'arrêter.\n" +
		"Pour reprendre cette session : codebox\n"
	if out != want {
		t.Errorf("exitScreen output =\n%q\nwant\n%q", out, want)
	}
	if b.stopped {
		t.Error("the machine was stopped without a question")
	}
}

func TestExitScreenNotRunningSkipsEverything(t *testing.T) {
	forceExitTTY(t, true) // the questions must be skipped because it is not running, not because of the pipe
	b := &exitScreenBackend{running: false}
	d := codebox.NewDispatcherWith(codebox.Config{}, map[codebox.Runtime]codebox.Backend{
		codebox.RuntimeMicrosandbox: b,
	})
	out := captureStdout(t, func() {
		exitScreen(context.Background(), d, codebox.Config{}, codebox.RuntimeMicrosandbox, "inst")
	})
	want := "\nSession terminée.\nPour reprendre cette session : codebox\n"
	if out != want {
		t.Errorf("exitScreen output =\n%q\nwant\n%q", out, want)
	}
}

func TestExitScreenWithoutBackendIsSilent(t *testing.T) {
	d := codebox.NewDispatcherWith(codebox.Config{}, nil)
	out := captureStdout(t, func() {
		exitScreen(context.Background(), d, codebox.Config{}, codebox.RuntimeTart, "inst")
	})
	if out != "" {
		t.Errorf("exitScreen printed %q without a backend", out)
	}
}

func TestWorkspaceApplyUsageListsTheCommand(t *testing.T) {
	setupTestEnv(t)
	var code int
	var err error
	usage := captureStderr(t, func() {
		code, err = workspaceCmd(nil, codebox.Config{}, "exit-test")
	})
	if code != 2 || err != nil {
		t.Fatalf("code=%d err=%v, want 2 and no error", code, err)
	}
	if !strings.Contains(usage, "apply [--all]") {
		t.Errorf("usage is missing apply:\n%s", usage)
	}
}

func TestWorkspaceApplyRejectsUnknownOption(t *testing.T) {
	setupTestEnv(t)
	var code int
	var err error
	code, err = workspaceCmd([]string{"apply", "--al"}, codebox.Config{}, "exit-test")
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if err == nil || err.Error() != "Unknown workspace apply option: --al (expected --all)" {
		t.Fatalf("err = %v, want the unknown-option rejection", err)
	}
}

// TestGuestBranchLine pins the exit screen's branch wording (#306): the
// states must stay distinct and none may announce a false state.
func TestGuestBranchLine(t *testing.T) {
	cases := []struct {
		name   string
		status codebox.GuestBranchStatus
		want   string
	}{
		{
			"nothing to report",
			codebox.GuestBranchStatus{PRKnown: true},
			"",
		},
		{
			"pushed with a draft PR",
			codebox.GuestBranchStatus{Branch: "my-fix", Pushed: true, PRKnown: true, PRURL: "https://github.com/o/r/pull/12", PRDraft: true},
			"Branche poussée : my-fix, pull request en brouillon : https://github.com/o/r/pull/12",
		},
		{
			"pushed with a ready PR is not called a draft",
			codebox.GuestBranchStatus{Branch: "my-fix", Pushed: true, PRKnown: true, PRURL: "https://github.com/o/r/pull/12", PRDraft: false},
			"Branche poussée : my-fix, pull request : https://github.com/o/r/pull/12",
		},
		{
			"pushed with an unverifiable PR state",
			codebox.GuestBranchStatus{Branch: "my-fix", Pushed: true, PRKnown: false},
			"Branche poussée : my-fix (pull request non vérifiée : gh ou réseau indisponible)",
		},
		{
			"pushed without a PR",
			codebox.GuestBranchStatus{Branch: "my-fix", Pushed: true, PRKnown: true},
			"Branche poussée : my-fix",
		},
		{
			"created but never pushed",
			codebox.GuestBranchStatus{Branch: "my-fix"},
			"Branche créée dans la machine isolée mais non poussée : my-fix",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := guestBranchLine(tc.status); got != tc.want {
				t.Errorf("guestBranchLine = %q, want %q", got, tc.want)
			}
		})
	}
}
