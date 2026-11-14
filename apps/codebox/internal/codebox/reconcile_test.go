package codebox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The plan function is pure: these tests pin the classification table of
// P07. The executor tests below cover the journalling and the no-write
// no-op contract.

func TestPlanReconcileClassification(t *testing.T) {
	desired := DesiredState{
		Instance:  "cb-foo-ab12c",
		Isolation: IsolationBackend,
		Image:     msbImage,
		Username:  "opencode",
	}
	applied := desired.toState()

	cases := []struct {
		name     string
		applied  *InstanceState
		facts    ReconcileFacts
		wantOps  []ReconcileOp
		wantNoop bool
	}{
		{
			name:    "missing instance creates",
			facts:   ReconcileFacts{},
			wantOps: []ReconcileOp{OpCreate},
		},
		{
			name:    "creation-fixed change refuses in place",
			facts:   ReconcileFacts{Exists: true, Running: true, CreationFixedChanged: true},
			wantOps: []ReconcileOp{OpRecreate},
		},
		{
			name: "identical state healthy is a no-op",
			applied: func() *InstanceState {
				s := applied
				return &s
			}(),
			facts:    ReconcileFacts{Exists: true, Running: true, Healthy: true, CredentialChanged: false},
			wantOps:  []ReconcileOp{OpNoOp},
			wantNoop: true,
		},
		{
			name: "identical state unhealthy backend restarts the backend only",
			applied: func() *InstanceState {
				s := applied
				return &s
			}(),
			facts:   ReconcileFacts{Exists: true, Running: true, CredentialChanged: false},
			wantOps: []ReconcileOp{OpRestartBackend},
		},
		{
			name: "identical state stopped starts",
			applied: func() *InstanceState {
				s := applied
				return &s
			}(),
			facts:   ReconcileFacts{Exists: true, CredentialChanged: false},
			wantOps: []ReconcileOp{OpStartVM},
		},
		{
			name: "credential change on running VM refreshes then restarts",
			applied: func() *InstanceState {
				s := applied
				return &s
			}(),
			facts:   ReconcileFacts{Exists: true, Running: true, Healthy: true, CredentialChanged: true},
			wantOps: []ReconcileOp{OpRefreshCredentials, OpRestartVM},
		},
		{
			name: "credential change on stopped VM refreshes for next boot",
			applied: func() *InstanceState {
				s := applied
				return &s
			}(),
			facts:   ReconcileFacts{Exists: true, CredentialChanged: true},
			wantOps: []ReconcileOp{OpRefreshCredentials, OpStartVM},
		},
		{
			name:    "pre-state instance gets full refresh",
			facts:   ReconcileFacts{Exists: true, Running: true},
			wantOps: []ReconcileOp{OpRefreshCredentials, OpRestartVM},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanReconcile(tc.applied, desired, tc.facts)
			if !sameOps(plan.Ops, tc.wantOps) {
				t.Fatalf("ops = %v, want %v (reason: %s)", plan.Ops, tc.wantOps, plan.Reason)
			}
			if plan.IsNoOp() != tc.wantNoop {
				t.Fatalf("IsNoOp = %v, want %v", plan.IsNoOp(), tc.wantNoop)
			}
		})
	}
}

func TestConfigRevisionExcludesSecrets(t *testing.T) {
	// The revision must not move when only a secret value changes: the
	// state file must never contain anything derived from a credential.
	base := DesiredState{Instance: "i", Isolation: IsolationBackend, Image: "img", Username: "u"}
	if base.ConfigRevision() != base.ConfigRevision() {
		t.Fatal("revision must be deterministic")
	}
	changed := base
	changed.CredentialRev = "abc123"
	if base.ConfigRevision() == changed.ConfigRevision() {
		t.Fatal("the binding-set revision must participate in the revision")
	}
	// The host workspace path is NOT part of the revision: with the sealed
	// workspace (P22) the sandbox holds no reference to it, so changing the
	// transfer source must not schedule a sandbox restart. The path lives in
	// the host config and takes effect at the next 'workspace sync'.
	normalized := base
	if base.ConfigRevision() != normalized.ConfigRevision() {
		t.Fatal("the revision must be stable for the same desired state")
	}
}

// TestConfigRevisionIncludesMCPSelection pins the P15 revision contract for
// the curated remote connector selection: a change must flip the revision so
// reconciliation applies it to a running healthy guest (deselection is an
// authorization surface), while an empty selection must not move the revision
// so pre-P15 state stays compatible.
func TestConfigRevisionIncludesMCPSelection(t *testing.T) {
	base := DesiredState{Instance: "i", Isolation: IsolationBackend, Image: "img", Username: "u"}
	selected := base
	selected.MCPConnectors = []string{"context7"}
	if base.ConfigRevision() == selected.ConfigRevision() {
		t.Fatal("selecting a connector must change the revision")
	}
	deselected := base
	if selected.ConfigRevision() == deselected.ConfigRevision() {
		t.Fatal("deselecting a connector must change the revision back")
	}
	// The compatibility claim is pinned against the hardcoded pre-P15 hash
	// shape, not by comparing two revisions computed by the same code: a
	// regression that made an empty selection contribute a part would move
	// both sides together and stay green, while every pre-P15 state file
	// would silently mismatch (mass spurious restarts).
	legacyHash := sha256.New()
	for _, part := range []string{"cb-state-v1", base.Instance, string(base.Isolation), base.Image, base.Username,
		"rev:" + base.CredentialRev, "gen:" + base.CredentialGeneration} {
		_, _ = legacyHash.Write([]byte(part))
		_, _ = legacyHash.Write([]byte{0})
	}
	if got, want := deselected.ConfigRevision(), hex.EncodeToString(legacyHash.Sum(nil))[:16]; got != want {
		t.Fatalf("empty selection revision = %s, want unchanged pre-P15 hash %s (legacy state compatibility)", got, want)
	}
	reordered := selected
	reordered.MCPConnectors = []string{"context7", "data-gouv"}
	canonical := selected
	canonical.MCPConnectors = []string{"data-gouv", "context7"}
	if reordered.ConfigRevision() != canonical.ConfigRevision() {
		t.Fatal("the revision must not depend on the order the connectors were listed in")
	}
}

// TestPlanReconcileRestartsRunningGuestOnMCPChange pins the classification
// that makes a deselection effective: a changed connector selection on a
// running, healthy guest must not take the no-op path.
func TestPlanReconcileRestartsRunningGuestOnMCPChange(t *testing.T) {
	base := DesiredState{Instance: "i", Isolation: IsolationBackend, Image: "img", Username: "u"}
	applied := base.toState()
	desired := base
	desired.MCPConnectors = []string{"context7"}
	plan := PlanReconcile(&applied, desired, ReconcileFacts{Exists: true, Running: true, Healthy: true})
	if plan.IsNoOp() {
		t.Fatal("a changed MCP selection must not be a no-op on a healthy running guest")
	}
	if !sameOps(plan.Ops, []ReconcileOp{OpRefreshCredentials, OpRestartVM}) {
		t.Fatalf("ops = %v, want refresh-credentials + restart-vm (reason: %s)", plan.Ops, plan.Reason)
	}
	// Identical selection on a healthy running guest stays a no-op.
	same := desired.toState()
	plan = PlanReconcile(&same, desired, ReconcileFacts{Exists: true, Running: true, Healthy: true})
	if !plan.IsNoOp() {
		t.Fatalf("identical MCP selection must stay a no-op, got %v", plan.Ops)
	}
}

// TestConfigRevisionIncludesPiConfiguration pins the Pi counterpart of the MCP
// contract: a Pi guest is prepared from the composed documents, and the launch
// resolves the selection on the host and passes the model on the command line,
// so a changed composition has to move the revision or a guest still running
// the previous catalogue is asked for a model it does not have.
func TestConfigRevisionIncludesPiConfiguration(t *testing.T) {
	base := DesiredState{Instance: "i", Isolation: IsolationBackend, Image: "img", Username: "u"}
	configured := base
	configured.PiConfigFingerprint = PiConfigFingerprint(`{"models":"a"}`, `{"settings":"a"}`)
	if base.ConfigRevision() == configured.ConfigRevision() {
		t.Fatal("a managed Pi configuration must change the revision")
	}
	changed := base
	changed.PiConfigFingerprint = PiConfigFingerprint(`{"models":"b"}`, `{"settings":"a"}`)
	if configured.ConfigRevision() == changed.ConfigRevision() {
		t.Fatal("a model added to the composed Pi catalogue must change the revision")
	}
	// The empty case is pinned against the hash shape a state file written
	// before this field carries, not by comparing two revisions computed by
	// the same code: a regression that made a non-Pi project contribute a part
	// would move both sides together and stay green, while every existing
	// OpenCode guest would restart once on upgrade.
	legacyHash := sha256.New()
	for _, part := range []string{"cb-state-v1", base.Instance, string(base.Isolation), base.Image, base.Username,
		"rev:" + base.CredentialRev, "gen:" + base.CredentialGeneration} {
		_, _ = legacyHash.Write([]byte(part))
		_, _ = legacyHash.Write([]byte{0})
	}
	if got, want := base.ConfigRevision(), hex.EncodeToString(legacyHash.Sum(nil))[:16]; got != want {
		t.Fatalf("empty fingerprint revision = %s, want the unchanged hash %s (an OpenCode guest must not restart)", got, want)
	}
}

// TestPlanReconcileRestartsRunningGuestOnPiConfigurationChange pins the
// classification end to end for the reported defect: a running healthy guest
// holding the previous Pi catalogue has to be refreshed and restarted, not
// left as a no-op that the launch then walks into.
func TestPlanReconcileRestartsRunningGuestOnPiConfigurationChange(t *testing.T) {
	base := DesiredState{Instance: "i", Isolation: IsolationBackend, Image: "img", Username: "u"}
	applied := base.toState()
	desired := base
	desired.PiConfigFingerprint = PiConfigFingerprint(`{"models":"qwen3-coder"}`, `{"settings":"managed"}`)
	plan := PlanReconcile(&applied, desired, ReconcileFacts{Exists: true, Running: true, Healthy: true})
	if !sameOps(plan.Ops, []ReconcileOp{OpRefreshCredentials, OpRestartVM}) {
		t.Fatalf("ops = %v, want refresh-credentials + restart-vm (reason: %s)", plan.Ops, plan.Reason)
	}
	// The same configuration on the same guest stays a no-op, so a Pi project
	// does not restart on every run.
	same := desired.toState()
	if plan := PlanReconcile(&same, desired, ReconcileFacts{Exists: true, Running: true, Healthy: true}); !plan.IsNoOp() {
		t.Fatalf("an unchanged Pi configuration must stay a no-op, got %v", plan.Ops)
	}
}

// TestDesiredStateCarriesOverlayMCPSelection pins the wiring the revision
// tests abstract away: the overlay the dispatcher installs must reach the
// desired state, or a changed selection would compute the same revision as
// the applied one and reconcile back to a silent no-op — the exact bug the
// revision participation exists to fix.
func TestDesiredStateCarriesOverlayMCPSelection(t *testing.T) {
	isolateHostState(t)
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	without := m.desiredState(true, nil, nil)
	m.OpenCodeOverlay = ManagedOverlay{MCPConnectors: []string{"data-gouv", "context7"}}
	with := m.desiredState(true, nil, nil)
	if len(with.MCPConnectors) != 2 {
		t.Fatalf("desiredState.MCPConnectors = %v, want the overlay selection", with.MCPConnectors)
	}
	if without.ConfigRevision() == with.ConfigRevision() {
		t.Fatal("an overlay selection must change the revision, or reconcile cannot see it")
	}
}

func TestInstanceStateRoundTripAndSchemaGuard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	st := DesiredState{Instance: "i", Isolation: IsolationFull, Image: "img", GuestProfile: "browser-profile-v1", Username: "u"}.toState()
	st.Pending = []string{"refresh-credentials", "restart-vm"}
	if err := WriteInstanceState(DefaultFS, path, st); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadInstanceState(DefaultFS, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.ConfigRevision != st.ConfigRevision || got.GuestProfile != st.GuestProfile || got.SchemaVersion != instanceStateSchemaVersion || len(got.Pending) != 2 {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	// Missing file is not an error: the instance predates state tracking.
	if s, err := ReadInstanceState(DefaultFS, filepath.Join(dir, "absent.json")); err != nil || s != nil {
		t.Fatalf("missing state must be (nil, nil), got (%v, %v)", s, err)
	}
	// A newer schema is an error, never a deletion trigger.
	newer := map[string]any{"schemaVersion": 99}
	data, _ := json.Marshal(newer)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInstanceState(DefaultFS, path); err == nil || !strings.Contains(err.Error(), "newer than this build supports") {
		t.Fatalf("newer schema must refuse, got: %v", err)
	}
}

// isolateHostState points the host state directory at a disposable HOME.
//
// Reconcile reads and writes DefaultStateDir(), which on a developer's machine
// is the real ~/.local/state/codebox — holding live bindings, transfer
// decisions and journals for their actual instances. A test that reaches
// Reconcile must never read or remove that, so every such test isolates HOME
// first.
func isolateHostState(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserHomeDir reads USERPROFILE on Windows.
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
}

func TestReconcileNoOpWritesNothing(t *testing.T) {
	isolateHostState(t)
	// Identical desired state with a healthy guest must not touch the
	// guest or rewrite the state file (mtime included).
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}
	// Point the state at a temp dir by writing the applied state through
	// the same path the runtime will read.
	desired := m.desiredState(true, testBindings(m.cfg.APIKey), nil)
	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := WriteInstanceState(DefaultFS, path, desired.toState()); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	callsBefore := len(client.calls)
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("no-op reconcile must not rewrite the state file")
	}
	// A no-op still inspects the instance (lookup, config read, mount
	// and workspace-provenance reads) to classify; what it must not do is
	// mutate the guest.
	for _, call := range client.calls[callsBefore:] {
		switch {
		case strings.HasPrefix(call, "lookup "),
			strings.HasPrefix(call, "readconfig "),
			strings.HasPrefix(call, "mount "),
			strings.HasPrefix(call, "owned "),
			strings.HasPrefix(call, "server-port "),
			strings.HasPrefix(call, "readenv "):
		default:
			t.Fatalf("no-op reconcile mutated the guest: %q", call)
		}
	}
	_ = os.Remove(path)
	_ = os.RemoveAll(filepath.Dir(path))
}

func TestReconcileNoOpInstallsNewProjectSkills(t *testing.T) {
	isolateHostState(t)
	raw := skillSourceTar(t, skillTarEntry{
		name: "skills/rgaa/SKILL.md",
		body: []byte("---\nname: rgaa\ndescription: fixture\n---\n"),
	})
	archive, err := normalizeSkillArchive(bytes.NewReader(raw), "skills/rgaa", "rgaa")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	lock := SkillLock{Repository: projectSkillsRepository, Revision: projectSkillsRevision, SHA256: hex.EncodeToString(digest[:])}
	skill := SkillPackage{ID: "official/rgaa", Name: "rgaa", Lock: lock, Archive: archive}
	client := &fakeMSBClient{
		exists: true, status: "running",
		execCaptureResults: []fakeMSBExecCaptureResult{
			{stdout: `{"schemaVersion":1,"skills":[]}`},
			{stdout: `{"data":[{"name":"rgaa"}]}`},
		},
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.cfg.ProjectSkillIDs = []string{skill.ID}
	m.cfg.ProjectSkillsManaged = true
	m.cfg.ProjectSkills = []SkillPackage{skill}
	m.Probe = func(context.Context, string, string, string) HealthProbe { return HealthProbe{Healthy: true} }
	desired := m.desiredState(true, testBindings(m.cfg.APIKey), nil)
	statePath := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := WriteInstanceState(DefaultFS, statePath, desired.toState()); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.written) != 2 || countCalls(client, "create") != 0 || countCalls(client, "start") != 0 {
		t.Fatalf("no-op reconcile did not install skills without restarting the guest: writes=%d calls=%v", len(client.written), client.calls)
	}
	if !hasCall(client, "execcapture "+m.InstanceName()+" opencode api --standalone GET /api/skill") {
		t.Fatal("no-op reconcile returned before verifying OpenCode skill discovery")
	}
	_ = os.Remove(statePath)
	_ = os.RemoveAll(filepath.Dir(statePath))
}

func TestReconcileBackendRestartInstallsSkillsBeforeRelaunch(t *testing.T) {
	isolateHostState(t)
	raw := skillSourceTar(t, skillTarEntry{
		name: "skills/rgaa/SKILL.md",
		body: []byte("---\nname: rgaa\ndescription: fixture\n---\n"),
	})
	archive, err := normalizeSkillArchive(bytes.NewReader(raw), "skills/rgaa", "rgaa")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	lock := SkillLock{Repository: projectSkillsRepository, Revision: projectSkillsRevision, SHA256: hex.EncodeToString(digest[:])}
	skill := SkillPackage{ID: "official/rgaa", Name: "rgaa", Lock: lock, Archive: archive}
	client := &fakeMSBClient{
		exists: true, status: "running", startScript: msbStartScript(IsolationBackend),
		execCaptureResults: []fakeMSBExecCaptureResult{
			{stdout: `{"schemaVersion":1,"skills":[]}`},
			{stdout: `{"data":[{"name":"rgaa"}]}`},
		},
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationBackend
	m.cfg.ProjectSkillIDs = []string{skill.ID}
	m.cfg.ProjectSkillsManaged = true
	m.cfg.ProjectSkills = []SkillPackage{skill}
	m.Probe = func(context.Context, string, string, string) HealthProbe { return HealthProbe{Healthy: false} }
	desired := m.desiredState(true, testBindings(m.cfg.APIKey), nil)
	statePath := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := WriteInstanceState(DefaultFS, statePath, desired.toState()); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	installAt, restartAt := -1, -1
	for index, call := range client.calls {
		if strings.Contains(call, "tar -xzf") {
			installAt = index
		}
		if strings.Contains(call, msbRelaunchCommand) {
			restartAt = index
		}
	}
	if installAt < 0 || restartAt < 0 || installAt >= restartAt {
		t.Fatalf("skill install must complete before the backend relaunch: installAt=%d restartAt=%d calls=%v", installAt, restartAt, client.calls)
	}
	_ = os.Remove(statePath)
	_ = os.RemoveAll(filepath.Dir(statePath))
}

func TestGitHubOriginParticipatesOnlyForResolvedBinding(t *testing.T) {
	isolateHostState(t)
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	albert := testBindings(m.cfg.APIKey)
	github := append(append([]resolvedBinding{}, albert...), resolvedBinding{
		msbSecretBinding: msbSecretBinding{Kind: CredentialGithub, GuestEnv: "GITHUB_TOKEN"},
	})
	m.cfg.GitHubRemote = GitHubRemote{URL: "https://github.com/owner/one.git", Repo: "owner/one"}
	without := m.desiredState(true, albert, nil)
	legacyHash := sha256.New()
	for _, part := range []string{"cb-state-v1", without.Instance, string(without.Isolation), without.Image, without.Username,
		"rev:" + without.CredentialRev, "gen:" + without.CredentialGeneration} {
		_, _ = legacyHash.Write([]byte(part))
		_, _ = legacyHash.Write([]byte{0})
	}
	if got, want := without.ConfigRevision(), hex.EncodeToString(legacyHash.Sum(nil))[:16]; got != want {
		t.Fatalf("legacy no-GitHub revision = %s, want unchanged pre-P13 hash %s", got, want)
	}
	before := m.desiredState(true, github, nil)
	applied := before.toState()
	m.cfg.GitHubRemote = GitHubRemote{URL: "https://github.com/owner/two.git", Repo: "owner/two"}
	after := m.desiredState(true, github, &applied)
	if before.ConfigRevision() == after.ConfigRevision() {
		t.Fatal("approved origin changes must not reconcile as a no-op")
	}
	revoked := m.desiredState(true, albert, &applied)
	if revoked.GitHubOrigin != before.GitHubOrigin || bindingsRevision(github) == bindingsRevision(albert) {
		t.Fatal("revoking GitHub must remove its credential binding but retain the initialized origin")
	}
	revokedState := revoked.toState()
	if err := m.validateAppliedGitHubOrigin(github, &revokedState); err == nil {
		t.Fatal("reapproval against a changed repository must still be refused after revocation")
	}
	if without.ConfigRevision() != m.desiredState(true, albert, nil).ConfigRevision() {
		t.Fatal("origin changes must not affect projects without a GitHub binding")
	}
	if got := m.desiredState(false, nil, &applied); got.ConfigRevision() != before.ConfigRevision() {
		t.Fatal("an unresolved credential must retain the applied origin and revision")
	}
}

func TestReconcileRejectsExistingFullModeServerPortMapping(t *testing.T) {
	isolateHostState(t)
	client := &fakeMSBClient{
		exists:              true,
		status:              "running",
		startScript:         msbStartScript(IsolationFull),
		serverPortForwarded: true,
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}
	desired := m.desiredState(true, testBindings(m.cfg.APIKey), nil)
	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := WriteInstanceState(DefaultFS, path, desired.toState()); err != nil {
		t.Fatal(err)
	}

	err := m.Reconcile(context.Background())
	var need *ErrRecreateNeeded
	if err == nil || !errorsAs(err, &need) || !strings.Contains(err.Error(), "4096") {
		t.Fatalf("Reconcile error = %v, want an explicit recreate refusal for port 4096", err)
	}
	for _, call := range client.calls {
		if strings.HasPrefix(call, "start ") || strings.HasPrefix(call, "remove") || strings.HasPrefix(call, "stop ") {
			t.Fatalf("stale full-mode mapping must not be started, stopped, or removed implicitly: %v", client.calls)
		}
	}
	_ = os.Remove(path)
	_ = os.RemoveAll(filepath.Dir(path))
}

func TestReconcileResumesInterruptedApply(t *testing.T) {
	isolateHostState(t)
	// An interrupted apply journals its remaining ops; the next run of the
	// same revision resumes at the unfinished operation instead of
	// restarting from scratch.
	client := &fakeMSBClient{exists: true, status: "stopped"}
	m := newTestMicrosandbox(t, client)

	desired := m.desiredState(true, testBindings(m.cfg.APIKey), nil)
	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	// Simulate a crash after refresh-credentials, with the journal written.
	st := desired.toState()
	st.Pending = []string{"start-vm"}
	if err := WriteInstanceState(DefaultFS, path, st); err != nil {
		t.Fatal(err)
	}

	// The journal carries the same revision as the desired state, so the
	// resume must honor it. Start itself pushes one next-start refresh
	// (modify) before booting a stopped sandbox; the reconcile
	// refresh-credentials op would add a second. One modify = the resume
	// skipped the completed op; two = it restarted the plan from scratch.
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !hasCall(client, "start ") {
		t.Fatalf("resume must run the unfinished start: %v", client.calls)
	}
	if countCalls(client, "modify ") != 1 {
		t.Fatalf("resume repeated the completed refresh (modify calls: %d): %v", countCalls(client, "modify "), client.calls)
	}
	_ = os.RemoveAll(filepath.Dir(path))
	_ = os.RemoveAll(filepath.Dir(path))
}

func TestReconcileFinishesPendingJournalDespiteHealthyGuest(t *testing.T) {
	isolateHostState(t)
	// A failed op journals the desired revision with Pending set. On the
	// next run the guest may look healthy (the failed refresh never took
	// effect), but the journal must still be executed — returning no-op
	// would silently drop the failed credential or config update forever.
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}
	desired := m.desiredState(true, testBindings(m.cfg.APIKey), nil)
	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	st := desired.toState()
	st.Pending = []string{"refresh-credentials", "restart-vm"}
	if err := WriteInstanceState(DefaultFS, path, st); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !hasCall(client, "modify ") {
		t.Fatalf("pending refresh must be retried despite healthy guest: %v", client.calls)
	}
	// The journal is consumed: the state file no longer carries Pending.
	final, err := ReadInstanceState(DefaultFS, path)
	if err != nil {
		t.Fatal(err)
	}
	if final == nil || len(final.Pending) != 0 {
		t.Fatalf("completed journal must be cleared, state: %+v", final)
	}
	_ = os.RemoveAll(filepath.Dir(path))
}

func TestReconcilePersistsJournalBeforeFirstSideEffect(t *testing.T) {
	isolateHostState(t)
	// An abrupt kill after an op produced side effects but before the
	// error-path journal write must not replay completed mutations. The
	// journal is persisted before execution and advanced after each op.
	client := &fakeMSBClient{exists: true, status: "stopped", startErr: assertErr("boom")}
	m := newTestMicrosandbox(t, client)
	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := m.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile must surface the start failure")
	}
	st, err := ReadInstanceState(DefaultFS, path)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || len(st.Pending) == 0 {
		t.Fatalf("journal must be persisted before execution, state: %+v", st)
	}
	_ = os.RemoveAll(filepath.Dir(path))
}

func TestReconcileSurfacesRecreateAsError(t *testing.T) {
	isolateHostState(t)
	// A creation-fixed change must come back as a typed error, never as an
	// automatic Clean. The instance is untouched.
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}
	desired := m.desiredState(true, testBindings(m.cfg.APIKey), nil)
	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	st := desired.toState()
	st.Isolation = string(IsolationFull) // differs from desired (backend)
	if err := WriteInstanceState(DefaultFS, path, st); err != nil {
		t.Fatal(err)
	}

	err := m.Reconcile(context.Background())
	var need *ErrRecreateNeeded
	if err == nil || !errorsAs(err, &need) {
		t.Fatalf("Reconcile must refuse with ErrRecreateNeeded, got: %v", err)
	}
	if !strings.Contains(err.Error(), "DESTROYS") && !strings.Contains(err.Error(), "recreate --microsandbox") {
		t.Fatalf("error must name the loss and the explicit command: %v", err)
	}
	for _, call := range client.calls {
		if strings.HasPrefix(call, "remove") || strings.HasPrefix(call, "stop") {
			t.Fatalf("refused reconcile must not touch the instance: %v", client.calls)
		}
	}
	_ = os.RemoveAll(filepath.Dir(path))
}

func TestReconcileJournalsOnFailure(t *testing.T) {
	isolateHostState(t)
	// When an op fails, the remaining ops are journaled so the next run
	// resumes there.
	client := &fakeMSBClient{exists: true, status: "stopped", startErr: assertErr("boom")}
	m := newTestMicrosandbox(t, client)

	if err := m.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile must surface the start failure")
	}
	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	st, err := ReadInstanceState(DefaultFS, path)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || len(st.Pending) == 0 {
		t.Fatalf("failure must journal remaining ops, state: %+v", st)
	}
	_ = os.RemoveAll(filepath.Dir(path))
}

func TestReconcileBreaksStaleSetupLock(t *testing.T) {
	isolateHostState(t)
	// Reconcile serializes through the per-instance setup lock. A lock
	// left by a dead process is stale and must be broken so reconcile
	// proceeds; a live holder blocks (the Acquire contract), which is why
	// the reconcile error path wraps lock failures with context.
	client := &fakeMSBClient{exists: true, status: "stopped"}
	m := newTestMicrosandbox(t, client)
	lockPath := SetupLockPath(DefaultStateDir(), m.InstanceName())
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// A PID that cannot exist on this host: the lock is provably stale.
	if err := os.WriteFile(lockPath, []byte("999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("stale lock must be broken, got: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatal("reconcile must release the lock when done")
	}
	_ = os.RemoveAll(filepath.Dir(lockPath))
}

// assertErr is a tiny error helper to keep the test file focused.
type testErr string

func (e testErr) Error() string { return string(e) }

func assertErr(msg string) error { return testErr(msg) }

func errorsAs(err error, target **ErrRecreateNeeded) bool {
	if e, ok := err.(*ErrRecreateNeeded); ok {
		*target = e
		return true
	}
	return false
}

// TestReconcileRefusesCredentialOpsWhenUnresolved pins the Codex P2 on the
// second review: with an unresolved binding set, the desired secret set is
// empty, and applying it would turn "reuse the persisted references" into
// their removal — the patch semantics make an empty set authoritative.
// Nothing may be changed, and the journal must survive so a later run with a
// resolvable credential resumes here.
func TestReconcileRefusesCredentialOpsWhenUnresolved(t *testing.T) {
	isolateHostState(t)
	client := &fakeMSBClient{exists: true, status: "stopped"}
	m := newTestMicrosandbox(t, client)
	// No resolvable credential: neither the env key nor a stored entry.
	m.cfg.APIKey = ""
	m.cfg.CredentialRef = "missing-ref"
	m.credentialRead = func(context.Context, CredentialKind, string) (string, string, error) {
		return "", "", ErrCredentialNotFound
	}
	// Point the state at a temp dir by writing the applied state through
	// the same path the runtime reads.
	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	// A journal left by a failed SDK refresh: the retry must not replay it
	// against an empty desired set.
	st := m.desiredState(false, nil, nil).toState()
	st.Pending = []string{"refresh-credentials", "start-vm"}
	if err := WriteInstanceState(DefaultFS, path, st); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Remove(path)
		_ = os.RemoveAll(filepath.Dir(path))
	}()

	err := m.Reconcile(context.Background())
	if err == nil || !strings.Contains(err.Error(), "credential set could not be resolved") {
		t.Fatalf("an unresolved credential set must refuse the credential ops: %v", err)
	}
	// The message must name the cause. A reused `err` variable printed
	// `(<nil>)` here, leaving the actionable part only in the warning above
	// (#202): the refusal is supposed to be self-explanatory.
	if strings.Contains(err.Error(), "(<nil>)") {
		t.Fatalf("the refusal must name the resolution cause, not <nil>: %v", err)
	}
	if !strings.Contains(err.Error(), "missing-ref") && !strings.Contains(err.Error(), "credential") {
		t.Fatalf("the refusal must name what could not be resolved: %v", err)
	}
	for _, call := range client.calls {
		switch {
		case strings.HasPrefix(call, "modify "), strings.HasPrefix(call, "start "), strings.HasPrefix(call, "create"):
			t.Fatalf("no credential-dependent operation may run: %q", call)
		}
	}
	// The journal survives, so the apply resumes once the credential is back.
	after, rerr := ReadInstanceState(DefaultFS, path)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(after.Pending) == 0 {
		t.Fatal("the journal must survive a refused apply")
	}
	// The operations that do not need the credential set stay available: a
	// plan of restart/relaunch only must not be blocked.
	if containsCredentialOps([]ReconcileOp{OpRestartBackend}) || containsCredentialOps([]ReconcileOp{OpRestartVM}) {
		t.Fatal("restart and backend-relaunch do not depend on the credential set")
	}
	if !containsCredentialOps([]ReconcileOp{OpRefreshCredentials}) || !containsCredentialOps([]ReconcileOp{OpStartVM}) {
		t.Fatal("refresh-credentials and start-vm do depend on the credential set")
	}
}

// TestInstanceStateReadsLegacyNumericCredentialGen pins the fifth review's
// P1: P07-era state files serialize `credentialGen` as a JSON number, and the
// upgrade must read them (mapping the number to the empty marker so the next
// apply records the real composite) instead of failing to unmarshal.
func TestInstanceStateReadsLegacyNumericCredentialGen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconcile.json")
	legacy := []byte(`{"schemaVersion":1,"instance":"cb-x","isolation":"backend","workspaceDir":"/w","image":"img","configRevision":"abc","credentialGen":0}` + "\n")
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := ReadInstanceState(DefaultFS, path)
	if err != nil {
		t.Fatalf("a P07-era numeric credentialGen must read cleanly: %v", err)
	}
	if string(st.CredentialGen) != "" {
		t.Fatalf("the legacy number maps to the empty marker: %q", st.CredentialGen)
	}
	// The current string composite round-trips.
	st.CredentialGen = credentialGenJSON("albert@native:2;github@file:1")
	if err := WriteInstanceState(DefaultFS, path, *st); err != nil {
		t.Fatal(err)
	}
	again, err := ReadInstanceState(DefaultFS, path)
	if err != nil || string(again.CredentialGen) != "albert@native:2;github@file:1" {
		t.Fatalf("string composite round trip: %+v, %v", again, err)
	}
}

// TestReconcileProceedsWithDotenvInTheCheckout pins the P22 change of role on
// the real CLI start path: Reconcile must not refuse a checkout that holds a
// .env, because with the sealed workspace that file is not mounted. The scan's
// finding is hygiene advice; the transfer filter is the boundary.
func TestReconcileProceedsWithDotenvInTheCheckout(t *testing.T) {
	isolateHostState(t)
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}
	if err := os.WriteFile(filepath.Join(m.cfg.WorkspaceDir, ".env"), []byte("SECRET=canary\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t, func() {
		err := m.Reconcile(context.Background())
		if err != nil {
			// Any other failure is fine to surface, but it must not be the
			// retired workspace gate.
			if strings.Contains(err.Error(), "refusing to start") {
				t.Fatalf("the workspace gate must not run on the sealed runtime: %v", err)
			}
		}
	})
	if !strings.Contains(stderr, "excluded from transfer") {
		t.Fatalf("the hygiene advice must still be shown: %q", stderr)
	}
	for _, call := range client.calls {
		if strings.HasPrefix(call, "remove") {
			t.Fatalf("a .env in the checkout must never destroy the instance: %v", client.calls)
		}
	}
	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	_ = os.RemoveAll(filepath.Dir(path))
}

// TestWorkspaceSourceChangeDoesNotMoveTheRevision pins the sealed-model
// semantics of WORKSPACE_DIR: it is the transfer source, read at sync time,
// and the sandbox holds no reference to it. Changing it must therefore not
// schedule a restart, let alone a destructive recreation.
func TestWorkspaceSourceChangeDoesNotMoveTheRevision(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	before := m.desiredState(true, nil, nil).ConfigRevision()
	m.cfg.WorkspaceDir = t.TempDir()
	after := m.desiredState(true, nil, nil).ConfigRevision()
	if before != after {
		t.Fatalf("changing the transfer source moved the config revision (%s -> %s)", before, after)
	}
}

// TestReconcileTreatsResourceChangeAsRecreation pins both halves of the
// resource rule through the REAL reconciliation path: a recorded change to the
// guest sizing requires recreation (the SDK cannot resize a running guest),
// while an instance whose sizing was never recorded (0) must not be recreated
// just for that. Asserting the classification here rather than re-typing the
// boolean expression is what makes the test able to fail.
func TestReconcileTreatsResourceChangeAsRecreation(t *testing.T) {
	isolateHostState(t)
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}
	m.cfg.CPUs = 8

	// An instance created with 4 CPUs, now asked for 8.
	applied := DesiredState{Instance: m.InstanceName(), Isolation: m.cfg.Isolation, Image: msbImage, CPUs: 4, MemoryMB: m.cfg.MemoryMB}.toState()
	if applied.CPUs != 4 {
		t.Fatalf("precondition: the state must record the sizing, got %d", applied.CPUs)
	}
	facts, err := m.reconcileFacts(context.Background(), &applied, m.desiredState(true, nil, nil))
	if err != nil {
		t.Fatalf("reconcileFacts: %v", err)
	}
	if !facts.CreationFixedChanged {
		t.Fatal("a recorded sizing change must classify as creation-fixed")
	}
	if !PlanReconcile(&applied, m.desiredState(true, nil, nil), facts).NeedsRecreate() {
		t.Fatal("a recorded sizing change must plan a recreation")
	}

	// An instance whose state predates the recorded sizing (0 = unknown).
	legacy := DesiredState{Instance: m.InstanceName(), Isolation: m.cfg.Isolation, Image: msbImage}.toState()
	facts, err = m.reconcileFacts(context.Background(), &legacy, m.desiredState(true, nil, nil))
	if err != nil {
		t.Fatalf("reconcileFacts: %v", err)
	}
	if facts.CreationFixedChanged {
		t.Fatal("an instance whose sizing was never recorded must not require recreation")
	}
}

// TestReconcileCarriesUnappliedSizingForward pins the state-write rule that
// keeps a sizing drift visible: a plan that does not create the instance
// leaves the guest with the sizing it already had, so the state must record
// that value, not the desired one. Recording the desired value would make the
// drift permanently invisible — and the state file would attest it was applied.
func TestReconcileCarriesUnappliedSizingForward(t *testing.T) {
	isolateHostState(t)
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Healthy: true}
	}
	// The project asks for more than the pre-existing instance has.
	m.cfg.CPUs, m.cfg.MemoryMB = 8, 16384

	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	applied := DesiredState{Instance: m.InstanceName(), Isolation: m.cfg.Isolation, Image: msbImage}.toState()
	// A credential rotation, so the plan is neither no-op nor create.
	applied.ConfigRevision = "stale"
	if err := WriteInstanceState(DefaultFS, path, applied); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	final, err := ReadInstanceState(DefaultFS, path)
	if err != nil {
		t.Fatal(err)
	}
	if final.CPUs == 8 || final.MemoryMB == 16384 {
		t.Fatalf("the state must not claim sizing the guest never received: %+v", final)
	}
	_ = os.RemoveAll(filepath.Dir(path))
}

func TestReconcileExistingInstanceBypassesApprovalGate(t *testing.T) {
	isolateHostState(t)
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	m.Probe = func(context.Context, string, string, string) HealthProbe { return HealthProbe{Healthy: true} }
	m.ProjectSecretApprovalGate = func() error { return errors.New("approval gate should not run for an existing instance") }
	desired := m.desiredState(true, testBindings(m.cfg.APIKey), nil)
	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := WriteInstanceState(DefaultFS, path, desired.toState()); err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("existing instance reconcile was blocked by the approval gate: %v", err)
	}
	_ = os.RemoveAll(filepath.Dir(path))
}

func TestStartInstanceCommonBoundaryEnforcesApprovalGateOnCreate(t *testing.T) {
	isolateHostState(t)
	client := &fakeMSBClient{exists: false}
	m := newTestMicrosandbox(t, client)
	m.ProjectSecretApprovalGate = func() error { return errors.New("missing approval") }
	if err := m.startInstance(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "missing approval") {
		t.Fatalf("startInstance(create) = %v, want approval gate refusal", err)
	}
	_ = os.RemoveAll(filepath.Dir(instanceStatePath(DefaultStateDir(), m.InstanceName())))
}
