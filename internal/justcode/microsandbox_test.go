package justcode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	msb "github.com/superradcompany/microsandbox/sdk/go"
)

type fakeMSBClient struct {
	status       string
	exists       bool
	lookupErr    error
	ensureErr    error
	doctorOutput string
	doctorErr    error
	mount        string
	mountErr     error
	// persistedSecretRefs are the env-reference secret registrations the
	// sandbox's persisted document carries. Recovery republishes them at boot
	// even when the current provider configuration differs, so this is what
	// drives the persisted-binding resolution path.
	persistedSecretRefs    []persistedSecretRef
	persistedSecretRefsErr error
	appliedPorts           []PortMapping
	appliedErr             error
	startScript            string
	env                    map[string]string
	envErr                 error
	createErr              error
	startErr               error
	modifyErr              error
	stopErr                error
	removeErr              error
	logsErr                error
	shellErr               error
	attachErr              error

	calls []string
	// startHostEnv captures the JUST_CODE_HOST_* transport variables that were
	// published (via withHostSecrets) while Start ran, keyed by variable name.
	startHostEnv     map[string]string
	created          *msbSandboxSpec
	modifiedEnv      map[string]string
	modifiedBindings []msbSecretBinding
	rotatedBindings  []msbSecretBinding
	removedSecrets   []string
	removedLive      bool
	execResults      []fakeMSBExecResult
	// execDefault is returned once execResults is drained; nil means success,
	// which preserves the fake's historical default.
	execDefault *fakeMSBExecResult
	// execCaptureResults drives ExecCapture (stdout is needed by change
	// delivery and by the guest-repository probes).
	execCaptureResults []fakeMSBExecCaptureResult
	execCaptureDefault *fakeMSBExecCaptureResult
	// written files pushed into the guest through WriteFile (P22 transfer).
	written  []fakeMSBWrite
	writeErr error
	// owned reports the /workspace provenance (P22): true means an owned
	// volume, i.e. a sealed workspace with no host mount. Nil means "derive
	// from mount": no host bind source means owned storage, which is what the
	// runtime actually persists for a sealed sandbox.
	owned               *bool
	ownedErr            error
	serverPortForwarded bool
	serverPortErr       error
	// listed/listedRunning drive List: the managed sandboxes the fake
	// runtime knows, and which of them are up.
	listed        []string
	listedRunning map[string]bool
}

type fakeMSBExecResult struct {
	code   int
	stderr string
	err    error
}

type fakeMSBExecCaptureResult struct {
	stdout string
	stderr string
	code   int
	err    error
}

type fakeMSBWrite struct {
	guestPath string
	data      []byte
}

func (f *fakeMSBClient) record(call string) { f.calls = append(f.calls, call) }

func (f *fakeMSBClient) EnsureInstalled(context.Context) error {
	f.record("ensure")
	return f.ensureErr
}

func (f *fakeMSBClient) Doctor(context.Context) (string, error) {
	f.record("doctor")
	return f.doctorOutput, f.doctorErr
}

func (f *fakeMSBClient) Lookup(_ context.Context, name string) (msbSandboxInfo, bool, error) {
	f.record("lookup " + name)
	return msbSandboxInfo{Name: name, Status: f.status}, f.exists, f.lookupErr
}

func (f *fakeMSBClient) Create(_ context.Context, spec msbSandboxSpec) error {
	f.record("create")
	copy := spec
	copy.Env = cloneStringMap(spec.Env)
	copy.Bindings = append([]msbSecretBinding(nil), spec.Bindings...)
	f.created = &copy
	// Creating a sandbox makes it exist: the provenance probes that follow
	// creation (P22) look the instance up.
	if f.createErr == nil {
		f.exists = true
	}
	return f.createErr
}

func (f *fakeMSBClient) AppliedPortMappings(_ context.Context, _ string) ([]PortMapping, error) {
	return f.appliedPorts, f.appliedErr
}

func (f *fakeMSBClient) Start(_ context.Context, name string) error {
	f.record("start " + name)
	if f.startHostEnv == nil {
		f.startHostEnv = map[string]string{}
	}
	for _, k := range [...]string{
		"JUST_CODE_HOST_ALBERT_API_KEY",
		"JUST_CODE_HOST_GITHUB_TOKEN",
		"JUST_CODE_HOST_CONTEXT7_API_KEY",
		"JUST_CODE_HOST_MISTRAL_API_KEY",
		"JUST_CODE_HOST_OPENAI_COMPATIBLE_API_KEY",
		"JUST_CODE_HOST_PROJECT_JC_API_KEY",
	} {
		f.startHostEnv[k] = os.Getenv(k)
	}
	return f.startErr
}

func (f *fakeMSBClient) ModifyNextStart(_ context.Context, name string, env map[string]string, bindings []msbSecretBinding, previouslyApplied []string) error {
	f.record("modify " + name)
	f.modifiedEnv = cloneStringMap(env)
	f.modifiedBindings = append([]msbSecretBinding(nil), bindings...)
	return f.modifyErr
}

func (f *fakeMSBClient) RotateSecretsLive(_ context.Context, name string, bindings []msbSecretBinding) error {
	f.record("rotate " + name)
	f.rotatedBindings = append([]msbSecretBinding(nil), bindings...)
	return f.modifyErr
}

func (f *fakeMSBClient) RemoveSecrets(_ context.Context, name string, guestEnvs []string, live bool) error {
	f.record("remove-secrets " + name)
	f.removedSecrets = append(f.removedSecrets, guestEnvs...)
	f.removedLive = live
	return f.modifyErr
}

func (f *fakeMSBClient) Exec(_ context.Context, name, command string) (int, string, error) {
	f.record("exec " + name + " " + command)
	if len(f.execResults) == 0 {
		if f.execDefault != nil {
			return f.execDefault.code, f.execDefault.stderr, f.execDefault.err
		}
		return 0, "", nil
	}
	result := f.execResults[0]
	f.execResults = f.execResults[1:]
	return result.code, result.stderr, result.err
}

func (f *fakeMSBClient) Stop(_ context.Context, name string) error {
	f.record("stop " + name)
	return f.stopErr
}

func (f *fakeMSBClient) Remove(_ context.Context, name string) error {
	f.record("remove " + name)
	return f.removeErr
}

func (f *fakeMSBClient) WorkspaceMount(_ context.Context, name string) (string, error) {
	f.record("mount " + name)
	return f.mount, f.mountErr
}

func (f *fakeMSBClient) WorkspaceOwned(_ context.Context, name string) (bool, error) {
	f.record("owned " + name)
	if f.ownedErr != nil {
		return false, f.ownedErr
	}
	if f.owned != nil {
		return *f.owned, nil
	}
	return f.mount == "", nil
}

func (f *fakeMSBClient) ServerPortForwarded(_ context.Context, name string) (bool, error) {
	f.record("server-port " + name)
	return f.serverPortForwarded, f.serverPortErr
}

func (f *fakeMSBClient) WriteFile(_ context.Context, name, guestPath string, data []byte) error {
	f.record("writefile " + name + " " + guestPath)
	if f.writeErr != nil {
		return f.writeErr
	}
	f.written = append(f.written, fakeMSBWrite{guestPath: guestPath, data: append([]byte(nil), data...)})
	return nil
}

func TestMicrosandboxInstallsPinnedSkillInOpenCodeGlobalDirectory(t *testing.T) {
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
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{
		{stdout: `{"schemaVersion":1,"skills":[]}`},
		{stdout: `{"data":[{"name":"rgaa"}]}`},
	}}
	m := NewMicrosandboxRuntime(Config{
		ProjectSkillIDs: []string{"official/rgaa"}, ProjectSkillsManaged: true,
		ProjectSkills: []SkillPackage{{ID: "official/rgaa", Name: "rgaa", Lock: lock, Archive: archive}},
	})
	m.Client = client
	if err := m.installProjectSkills(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(client.written) != 2 || client.written[0].guestPath != "/tmp/just-code-project-skills.json" ||
		client.written[1].guestPath != "/tmp/just-code-skill-rgaa-"+lock.SHA256+".tar.gz" {
		t.Fatalf("guest writes = %+v", client.written)
	}
	if !strings.Contains(string(client.written[0].data), "official/rgaa") {
		t.Fatal("project selection inventory was not staged before package installation")
	}
	if !bytes.Equal(client.written[1].data, archive) {
		t.Fatal("guest payload differs from the verified lock archive")
	}
	var installCommand string
	for _, call := range client.calls {
		if strings.Contains(call, "tar -xzf") {
			installCommand = call
		}
	}
	for _, expected := range []string{"${XDG_CONFIG_HOME", "parent=\"$config_home/opencode/skills\"", "dest=\"$parent/rgaa\"", "sha256sum -c", "tar -xzf", "refusing to overwrite"} {
		if !strings.Contains(installCommand, expected) {
			t.Fatalf("guest install command lacks %q: %s", expected, installCommand)
		}
	}
	if strings.Contains(installCommand, "/workspace/.opencode/skills") {
		t.Fatalf("guest skill was installed in the agent checkout: %s", installCommand)
	}
	if !hasCall(client, "execcapture "+m.InstanceName()+" opencode api --standalone GET /api/skill") {
		t.Fatal("startup did not verify that OpenCode discovered the installed skill")
	}
}

func TestGuestSkillInventoryScriptRejectsOrphanedManagedDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guest inventory script requires POSIX path semantics")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	home := t.TempDir()
	run := func() ([]byte, error) {
		cmd := exec.Command(sh, "-c", readGuestSkillInventoryScript(HarnessOpenCode))
		cmd.Env = []string{"HOME=" + home}
		return cmd.CombinedOutput()
	}
	output, err := run()
	if err != nil || strings.TrimSpace(string(output)) != `{"schemaVersion":1,"skills":[]}` {
		t.Fatalf("fresh guest inventory = %q, err = %v", output, err)
	}
	managedDir := filepath.Join(home, ".config", "opencode", "skills", "rgaa")
	if err := os.MkdirAll(managedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(managedDir, ".just-code-source"), []byte("pinned-source"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err = run()
	if err == nil || !strings.Contains(string(output), "managed skill directory exists without its project-skill inventory") {
		t.Fatalf("orphaned managed skill was accepted: output = %q, err = %v", output, err)
	}
}

func TestGuestSkillPreflightFindsManagedDirectoriesOmittedFromInventory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guest preflight script requires POSIX path semantics")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell is unavailable")
	}
	home := t.TempDir()
	run := func(allowedNames []string) ([]byte, error) {
		cmd := exec.Command(sh, "-c", guestSkillPathsPrelude(HarnessOpenCode)+guestSkillInventoryOrphanCheckScript(allowedNames))
		cmd.Env = []string{"HOME=" + home}
		return cmd.CombinedOutput()
	}
	managedDir := filepath.Join(home, ".config", "opencode", "skills", "rgaa")
	if err := os.MkdirAll(managedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := run(nil); err != nil || len(output) != 0 {
		t.Fatalf("unmarked user directory should pass an empty-inventory preflight: output=%q err=%v", output, err)
	}
	if err := os.WriteFile(filepath.Join(managedDir, ".just-code-source"), []byte("pinned-source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := run(nil); err == nil || !strings.Contains(string(output), "project-skill inventory") {
		t.Fatalf("managed directory omitted from an empty inventory was accepted: output=%q err=%v", output, err)
	}
	if output, err := run([]string{"rgaa"}); err != nil || len(output) != 0 {
		t.Fatalf("managed directory listed in the inventory should pass the orphan check: output=%q err=%v", output, err)
	}
	client := &fakeMSBClient{}
	m := NewMicrosandboxRuntime(Config{ProjectSkillsManaged: true})
	m.Client = client
	if err := m.preflightGuestSkills(context.Background(), guestSkillInventory{SchemaVersion: guestSkillInventorySchema}, nil); err != nil {
		t.Fatalf("empty-selection guest preflight: %v", err)
	}
	if len(client.calls) != 1 || !strings.Contains(client.calls[0], "for dest in \"$parent\"/*") || !strings.Contains(client.calls[0], ".just-code-source") {
		t.Fatalf("guest preflight omitted its managed-directory scan: %v", client.calls)
	}
}

func TestLocalOnlyInstructionPreflightRetriesAndExplainsRecovery(t *testing.T) {
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{
		{err: errors.New("guest agent is not ready")},
		{stdout: "before\n<!-- BEGIN JUST-CODE MANAGED SKILLS -->\n- `official/rgaa` at `" + projectSkillsRevision + "`\n<!-- END JUST-CODE MANAGED SKILLS -->\nafter\n"},
	}}
	m := NewMicrosandboxRuntime(Config{ProjectSkillsManaged: true, ProjectSkillsLocalOnly: true})
	m.Client = client
	m.launchRetryDelay = time.Millisecond
	err := m.preflightLocalOnlySkillInstructions(context.Background())
	if err == nil {
		t.Fatal("versioned managed instructions should block local-only startup")
	}
	for _, expected := range []string{"workspace export", "workspace sync", "while the guest is running", "just-code recreate"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("recovery error %q does not mention %q", err, expected)
		}
	}
	if len(client.calls) != 2 {
		t.Fatalf("guard attempts = %d, want retry after one transient error: %v", len(client.calls), client.calls)
	}
	if len(client.written) != 0 {
		t.Fatalf("read-only instruction preflight wrote guest data: %+v", client.written)
	}
}

func TestEmptySelectionFailsClosedWhenGuestInventoryIsMissing(t *testing.T) {
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{{
		stderr: "managed skill directory exists without its project-skill inventory", code: 1,
	}}}
	m := NewMicrosandboxRuntime(Config{ProjectSkillsManaged: true})
	m.Client = client
	err := m.installProjectSkills(context.Background())
	if err == nil || !strings.Contains(err.Error(), "project-skill inventory") {
		t.Fatalf("empty selection should reject an orphaned managed directory, got %v", err)
	}
	if len(client.written) != 0 || hasCall(client, "execcapture "+m.InstanceName()+" opencode api --standalone GET /api/skill") {
		t.Fatalf("reconciliation continued despite missing inventory: writes=%+v calls=%+v", client.written, client.calls)
	}
	if len(client.calls) != 1 || !strings.Contains(client.calls[0], "for marker in ") || !strings.Contains(client.calls[0], ".just-code-source") {
		t.Fatalf("inventory read did not check for orphaned managed directories: %+v", client.calls)
	}
}

func TestReadGuestSkillInventoryRetriesTransientGuestAgentError(t *testing.T) {
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{
		{err: errors.New("guest agent is not ready")},
		{stdout: `{"schemaVersion":1,"skills":[]}`},
	}}
	m := NewMicrosandboxRuntime(Config{ProjectSkillsManaged: true})
	m.Client = client
	m.launchRetryDelay = time.Millisecond

	inventory, err := m.readGuestSkillInventory(context.Background())
	if err != nil {
		t.Fatalf("transient guest-agent error should be retried: %v", err)
	}
	if inventory.SchemaVersion != guestSkillInventorySchema || len(inventory.Skills) != 0 {
		t.Fatalf("inventory = %+v", inventory)
	}
	if len(client.calls) != 2 {
		t.Fatalf("inventory attempts = %d, want 2: %v", len(client.calls), client.calls)
	}
}

func TestProjectSkillInstallRecoversAfterInventoryCommit(t *testing.T) {
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
	indexed := `{"schemaVersion":1,"skills":[{"id":"official/rgaa","name":"rgaa","revision":"` + lock.Revision + `","sha256":"` + lock.SHA256 + `"}]}`
	client := &fakeMSBClient{
		execResults: []fakeMSBExecResult{{}, {}, {code: 1, stderr: "simulated install failure"}},
		execCaptureResults: []fakeMSBExecCaptureResult{
			{stdout: `{"schemaVersion":1,"skills":[]}`},
			{stdout: indexed},
			{stdout: `{"data":[{"name":"rgaa"}]}`},
		},
	}
	m := NewMicrosandboxRuntime(Config{
		ProjectSkillIDs: []string{"official/rgaa"}, ProjectSkillsManaged: true,
		ProjectSkills: []SkillPackage{{ID: "official/rgaa", Name: "rgaa", Lock: lock, Archive: archive}},
	})
	m.Client = client
	if err := m.installProjectSkills(context.Background()); err == nil || !strings.Contains(err.Error(), "simulated install failure") {
		t.Fatalf("first install should fail after committing inventory, got %v", err)
	}
	if err := m.installProjectSkills(context.Background()); err != nil {
		t.Fatalf("retry should install the selected skill without rewriting its committed inventory: %v", err)
	}
	inventoryWrites := 0
	for _, write := range client.written {
		if write.guestPath == "/tmp/just-code-project-skills.json" {
			inventoryWrites++
		}
	}
	if inventoryWrites != 1 {
		t.Fatalf("inventory writes = %d, want one commit before the interrupted install", inventoryWrites)
	}
}

func TestVerifyProjectSkillsRejectsDuplicateDiscovery(t *testing.T) {
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{{
		stdout: `{"data":[{"name":"rgaa"},{"name":"rgaa"}]}`,
	}}}
	m := NewMicrosandboxRuntime(Config{ProjectSkills: []SkillPackage{{Name: "rgaa"}}})
	m.Client = client
	err := m.verifyProjectSkills(context.Background())
	if err == nil || !strings.Contains(err.Error(), `reports the pinned skill "rgaa" more than once`) {
		t.Fatalf("duplicate discovery error = %v", err)
	}
}

func TestSkillDeselectionRequiresExplicitGuestRecreation(t *testing.T) {
	old := guestSkillInventory{SchemaVersion: guestSkillInventorySchema, Skills: []guestSkillEntry{{
		ID: "official/rgaa", Name: "rgaa", Revision: projectSkillsRevision, SHA256: strings.Repeat("a", 64),
	}}}
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{{
		stdout: `{"schemaVersion":1,"skills":[{"id":"official/rgaa","name":"rgaa","revision":"` + projectSkillsRevision + `","sha256":"` + strings.Repeat("a", 64) + `"}]}`,
	}}}
	m := NewMicrosandboxRuntime(Config{ProjectSkillsManaged: true})
	m.Client = client
	if err := m.installProjectSkills(context.Background()); err == nil || !strings.Contains(err.Error(), "just-code recreate") {
		t.Fatalf("deselection error = %v", err)
	}
	if len(client.written) != 0 {
		t.Fatalf("deselection changed guest files before the explicit recreation: %+v", client.written)
	}
	if err := ensureGuestSkillSelectionCompatible(old, nil); err == nil || !strings.Contains(err.Error(), "just-code recreate") {
		t.Fatalf("deselection compatibility error = %v", err)
	}
	if err := ensureGuestSkillSelectionCompatible(old, map[string]SkillPackage{
		"official/rgaa": {ID: "official/rgaa", Name: "rgaa", Lock: SkillLock{Revision: projectSkillsRevision, SHA256: strings.Repeat("a", 64)}},
	}); err != nil {
		t.Fatalf("unchanged selected skill: %v", err)
	}
}

func TestLocalOnlySkillsRefuseStaleManagedInstructions(t *testing.T) {
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{
		{stdout: `{"schemaVersion":1,"skills":[]}`},
		{stdout: "user-authored instructions\n\n" + `<!-- BEGIN JUST-CODE MANAGED SKILLS -->` + "\n" +
			"## Managed project skills\n\n- `official/rgaa` at `" + projectSkillsRevision + "`\n" +
			`<!-- END JUST-CODE MANAGED SKILLS -->` + "\n"},
	}}
	m := NewMicrosandboxRuntime(Config{ProjectSkillsManaged: true, ProjectSkillsLocalOnly: true})
	m.Client = client

	err := m.installProjectSkills(context.Background())
	if err == nil || !strings.Contains(err.Error(), "just-code workspace sync") {
		t.Fatalf("stale guest instructions should require an explicit workspace refresh: %v", err)
	}
	if len(client.written) != 0 || len(client.calls) != 2 {
		t.Fatalf("stale instructions should block before any guest mutation: writes=%+v calls=%+v", client.written, client.calls)
	}
}

func (f *fakeMSBClient) ExecCapture(_ context.Context, name, command string) (string, string, int, error) {
	f.record("execcapture " + name + " " + command)
	return f.nextExecCapture()
}

func (f *fakeMSBClient) ExecCaptureStdin(_ context.Context, name, command string, _ []byte) (string, string, int, error) {
	f.record("execcapturestdin " + name + " " + command)
	return f.nextExecCapture()
}

// nextExecCapture serves the queued canned results, then the default. Both
// exec forms share it so a test that queues one result does not have to know
// which form the code under test chose.
func (f *fakeMSBClient) nextExecCapture() (string, string, int, error) {
	if len(f.execCaptureResults) == 0 {
		if f.execCaptureDefault != nil {
			return f.execCaptureDefault.stdout, f.execCaptureDefault.stderr, f.execCaptureDefault.code, f.execCaptureDefault.err
		}
		return "", "", 0, nil
	}
	result := f.execCaptureResults[0]
	f.execCaptureResults = f.execCaptureResults[1:]
	return result.stdout, result.stderr, result.code, result.err
}

func (f *fakeMSBClient) StartScript(_ context.Context, name string) (string, error) {
	// Recorded as "readconfig", not "start*": hasCall prefix-matches, so a
	// "start" prefix would be indistinguishable from a Start() call.
	f.record("readconfig " + name)
	return f.startScript, nil
}

func (f *fakeMSBClient) Env(_ context.Context, name string) (map[string]string, error) {
	f.record("readenv " + name)
	return cloneStringMap(f.env), f.envErr
}

func (f *fakeMSBClient) PersistedSecretRefs(_ context.Context, name string) ([]persistedSecretRef, error) {
	f.record("persistedsecretrefs " + name)
	return append([]persistedSecretRef(nil), f.persistedSecretRefs...), f.persistedSecretRefsErr
}

func (f *fakeMSBClient) List(_ context.Context) ([]msbSandboxInfo, error) {
	f.record("list")
	var out []msbSandboxInfo
	for _, name := range f.listed {
		status := "stopped"
		if f.listedRunning[name] {
			status = "running"
		}
		out = append(out, msbSandboxInfo{Name: name, Status: status})
	}
	return out, nil
}

func (f *fakeMSBClient) Logs(name string) error {
	f.record("logs " + name)
	return f.logsErr
}

func (f *fakeMSBClient) Shell(name string) error {
	f.record("shell " + name)
	return f.shellErr
}

func (f *fakeMSBClient) AttachInteractive(_ context.Context, name, cmd string, args []string, cwd string) (int, error) {
	f.record("attach " + name + " " + strings.Join(append([]string{cmd}, args...), " ") + " " + cwd)
	return 0, f.attachErr
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func newTestMicrosandbox(t *testing.T, client *fakeMSBClient) *MicrosandboxRuntime {
	t.Helper()
	m := NewMicrosandboxRuntime(Config{
		APIKey:       "key",
		WorkspaceDir: t.TempDir(),
		Username:     "opencode",
		Password:     "pw",
	})
	m.Client = client
	// Approvals live in host state; tests must never read the real one.
	m.StateDir = t.TempDir()
	m.launchRetryDelay = time.Millisecond
	// Default to "backend not healthy" so tests exercise the relaunch path.
	m.Probe = func(context.Context, string, string, string) HealthProbe { return HealthProbe{} }
	// A fresh fake reports the guest workspace as already provisioned, so
	// tests that are not about the sealed transfer do not run it. Tests that
	// exercise provisioning or refresh set execCaptureResults explicitly.
	if client.execCaptureDefault == nil {
		client.execCaptureDefault = &fakeMSBExecCaptureResult{stdout: "yes"}
	}
	return m
}

// testBindings builds the binding set the runtime resolves with a legacy
// env-sourced Albert credential: the required albert binding only.
func testBindings(value string) []resolvedBinding {
	b, _ := bindingForKind(CredentialAlbert)
	return []resolvedBinding{{msbSecretBinding: b, source: bindingSourceEnv, value: value}}
}

func countCalls(client *fakeMSBClient, prefix string) int {
	n := 0
	for _, c := range client.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func hasCall(client *fakeMSBClient, prefix string) bool {
	for _, call := range client.calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = write
	defer func() { os.Stdout = original }()

	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(read)
		done <- string(data)
	}()

	fn()
	write.Close()
	out := <-done
	read.Close()
	return out
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stderr
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = write
	defer func() { os.Stderr = original }()

	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(read)
		done <- string(data)
	}()

	fn()
	write.Close()
	out := <-done
	read.Close()
	return out
}

func TestMicrosandboxStartRequiresAPIKey(t *testing.T) {
	client := &fakeMSBClient{}
	m := NewMicrosandboxRuntime(Config{WorkspaceDir: t.TempDir()})
	m.Client = client
	m.StateDir = t.TempDir()
	// The credential store is not what this test exercises: inject the
	// resolution seam so a missing key is reported as missing, not as a
	// store-unreachable error on hosts without a native store.
	m.credentialRead = func(context.Context, CredentialKind, string) (string, string, error) {
		return "", "", ErrCredentialNotFound
	}
	if err := m.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "ALBERT_API_KEY") {
		t.Fatalf("expected API key error, got %v", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("SDK called before validation: %v", client.calls)
	}
}

func TestMicrosandboxStartNew(t *testing.T) {
	client := &fakeMSBClient{}
	m := newTestMicrosandbox(t, client)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if client.created == nil {
		t.Fatalf("sandbox was not created; calls: %v", client.calls)
	}
	spec := client.created
	if spec.Image != msbImage {
		t.Fatalf("created spec = %+v", spec)
	}
	// P22: the created spec must select the sealed workspace. The host
	// checkout is never named in the spec, so there is no host path the
	// runtime could bind.
	if !spec.SealedWorkspace {
		t.Fatalf("the created spec must request the sealed workspace: %+v", spec)
	}
	if len(spec.Bindings) != 1 || spec.Bindings[0].GuestEnv != msbAPISecretEnv ||
		!reflect.DeepEqual(spec.Bindings[0].AllowHosts, []string{msbAllowHost}) {
		t.Fatalf("binding config = %+v", spec.Bindings)
	}
	// The spec carries binding metadata only; the value transits through the
	// host env reference, and the create-time sentinel is rotated to it right
	// after creation, before the start script runs.
	if !hasCall(client, "rotate "+msbSandbox) {
		t.Fatalf("the bootstrap sentinel was not rotated to the env reference; calls: %v", client.calls)
	}
	if len(client.rotatedBindings) != 1 || client.rotatedBindings[0].HostEnv == "" {
		t.Fatalf("rotate bindings = %+v", client.rotatedBindings)
	}
	if spec.Env["OPENCODE_SERVER_PASSWORD"] != "pw" || spec.Env["OPENCODE_SERVER_USERNAME"] != "opencode" {
		t.Fatalf("credentials missing from guest env: %v", spec.Env)
	}
	if _, exists := spec.Env["ALBERT_API_KEY"]; exists {
		t.Fatal("real API key must not be placed in the guest environment")
	}
	if !strings.Contains(spec.StartScript, "opencode serve") || !strings.Contains(spec.Env["OPENCODE_CONFIG_CONTENT"], "albert.api.etalab.gouv.fr") {
		t.Fatal("embedded guest assets are missing")
	}
	// Creation boots an idle VM (the Go SDK has no background launch intent),
	// so the start script must be launched explicitly right after Create.
	if !hasCall(client, "exec "+msbSandbox+" "+msbRelaunchCommand) {
		t.Fatalf("start script was not launched after creation; calls: %v", client.calls)
	}
}

// TestMicrosandboxFullModeCreatePreparesGuest pins the full-mode half of the
// create fix: the same idle-VM problem leaves a freshly created full-mode
// sandbox without its toolchain, so the start script (which installs it and
// then keeps the VM alive) must run there too.
func TestMicrosandboxFullModeCreatePreparesGuest(t *testing.T) {
	client := &fakeMSBClient{}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if client.created == nil {
		t.Fatalf("sandbox was not created; calls: %v", client.calls)
	}
	if !hasCall(client, "exec "+msbSandbox+" "+msbRelaunchCommand) {
		t.Fatalf("start script was not launched after full-mode creation; calls: %v", client.calls)
	}
}

func TestMicrosandboxStartStopsOnInstallFailure(t *testing.T) {
	client := &fakeMSBClient{ensureErr: errors.New("download failed")}
	m := newTestMicrosandbox(t, client)
	err := m.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "download failed") {
		t.Fatalf("Start error = %v", err)
	}
	if hasCall(client, "lookup") || hasCall(client, "create") {
		t.Fatalf("continued after installation failure: %v", client.calls)
	}
}

func TestMicrosandboxStartRunningHealthy(t *testing.T) {
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	m.Probe = func(context.Context, string, string, string) HealthProbe {
		return HealthProbe{Status: 200, Healthy: true}
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// A healthy running backend must not be relaunched. The provisioning
	// probe (execcapture) is expected: it is how the runtime learns the
	// sealed workspace is already in place, and with it reporting "yes" no
	// transfer follows.
	for _, prefix := range []string{"create", "start ", "modify", "exec "} {
		if hasCall(client, prefix) {
			t.Fatalf("%s must not run for a healthy backend; calls: %v", prefix, client.calls)
		}
	}
}

func TestMicrosandboxStartRunningDeadBackend(t *testing.T) {
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !hasCall(client, "exec "+msbSandbox+" "+msbRelaunchCommand) {
		t.Fatalf("entrypoint was not relaunched: %v", client.calls)
	}
	if hasCall(client, "create") || hasCall(client, "start") {
		t.Fatalf("running VM was recreated or restarted: %v", client.calls)
	}
}

func TestMicrosandboxStartStoppedRefreshesSecretAndRelaunches(t *testing.T) {
	client := &fakeMSBClient{exists: true, status: "stopped"}
	m := newTestMicrosandbox(t, client)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(client.modifiedBindings) != 1 || client.modifiedBindings[0].GuestEnv != msbAPISecretEnv {
		t.Fatalf("the albert binding was not refreshed for next start: %+v", client.modifiedBindings)
	}
	if client.modifiedBindings[0].HostEnv == "" {
		t.Fatalf("the refresh must use the host env reference transport: %+v", client.modifiedBindings[0])
	}
	if client.modifiedEnv["OPENCODE_SERVER_PASSWORD"] != "pw" {
		t.Fatalf("credentials were not refreshed: %v", client.modifiedEnv)
	}
	for _, prefix := range []string{"modify " + msbSandbox, "start " + msbSandbox, "exec " + msbSandbox} {
		if !hasCall(client, prefix) {
			t.Fatalf("missing %q; calls: %v", prefix, client.calls)
		}
	}
	if hasCall(client, "create") {
		t.Fatalf("stopped sandbox was recreated: %v", client.calls)
	}
}

func TestMicrosandboxLaunchBackendRetries(t *testing.T) {
	client := &fakeMSBClient{execResults: []fakeMSBExecResult{
		{code: 1, stderr: "agent not ready"},
		{err: errors.New("agent unavailable")},
		{},
	}}
	m := newTestMicrosandbox(t, client)
	if err := m.launchBackend(context.Background()); err != nil {
		t.Fatalf("launchBackend: %v", err)
	}
	if got := len(client.calls); got != 3 {
		t.Fatalf("calls = %v, want three exec attempts", client.calls)
	}
}

// TestMSBRelaunchCommandRunsThroughShell pins the ENOEXEC half of the fix: the
// Go SDK stores script bodies verbatim, and sandboxes created before the
// shebang was added to guest-prep.sh persist a script that cannot be exec'd
// directly. The relaunch must therefore invoke the interpreter explicitly.
func TestMSBRelaunchCommandRunsThroughShell(t *testing.T) {
	if !strings.Contains(msbRelaunchCommand, "sh "+msbGuestEntrypoint) {
		t.Fatalf("relaunch must run the entrypoint through sh: %q", msbRelaunchCommand)
	}
	// A bare `nohup ... & sleep 1` always reports the sleep's exit status (0);
	// only the kill -0 on the launcher PID makes an immediate death visible.
	if !strings.Contains(msbRelaunchCommand, "pid=$!; sleep 1; kill -0 \"$pid\"") {
		t.Fatalf("relaunch must check the backgrounded launcher's survival: %q", msbRelaunchCommand)
	}
}

func TestMicrosandboxFullModeCreateWaitsForToolchain(t *testing.T) {
	// The relaunch succeeds, the marker check fails twice, then appears:
	// creation must block until the guest is actually prepared.
	client := &fakeMSBClient{execResults: []fakeMSBExecResult{
		{code: 0},            // relaunch
		{code: 1}, {code: 1}, // marker not yet present
		{code: 0}, // marker present
	}}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.toolchainPollDelay = time.Millisecond
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	markerChecks := 0
	for _, c := range client.calls {
		if c == "exec "+msbSandbox+" "+msbGuestPrepareProbe {
			markerChecks++
		}
	}
	if markerChecks != 3 {
		t.Fatalf("marker checked %d times, want 3; calls: %v", markerChecks, client.calls)
	}
}

func TestMicrosandboxFullModeCreateTimesOutWaitingForToolchain(t *testing.T) {
	// The installer never finishes: creation must fail with guidance, not
	// report success into an unprepared guest.
	client := &fakeMSBClient{
		execResults: []fakeMSBExecResult{{code: 0}},               // relaunch
		execDefault: &fakeMSBExecResult{code: msbPrepareProgress}, // never ready
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.cfg.StartTimeout = 20 * time.Millisecond
	m.toolchainPollDelay = time.Millisecond
	err := m.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "toolchain") {
		t.Fatalf("Start error = %v, want a toolchain readiness timeout", err)
	}
}

// A running full-mode sandbox can still be unprepared: a previous creation
// may have timed out while the background installer kept going. The running
// fast path must re-check readiness rather than attach into that guest.
func TestMicrosandboxFullModeRunningRechecksToolchain(t *testing.T) {
	client := &fakeMSBClient{
		exists:      true,
		status:      "running",
		startScript: msbStartScript(IsolationFull),
		execDefault: &fakeMSBExecResult{code: msbPrepareProgress}, // never ready
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.cfg.StartTimeout = 20 * time.Millisecond
	m.toolchainPollDelay = time.Millisecond
	err := m.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "toolchain") {
		t.Fatalf("Start error = %v, want a toolchain readiness timeout", err)
	}
	if hasCall(client, "exec "+msbSandbox+" "+msbRelaunchCommand) {
		t.Fatalf("an installer that is alive must not be joined by a second launch: %v", client.calls)
	}
}

// TestMicrosandboxFullModeRejectsPersistedServerPortMapping pins the upgrade
// path: a full-mode sandbox created before the port fix retains its persisted
// server mapping and must be explicitly recreated rather than silently
// adopted or started again.
func TestMicrosandboxFullModeRejectsPersistedServerPortMapping(t *testing.T) {
	client := &fakeMSBClient{
		exists:              true,
		status:              "stopped",
		startScript:         msbStartScript(IsolationFull),
		serverPortForwarded: true,
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	err := m.startInstance(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "recreate --microsandbox") || !strings.Contains(err.Error(), "4096") {
		t.Fatalf("start error = %v, want an explicit recreation instruction for port 4096", err)
	}
	if hasCall(client, "start "+msbSandbox) || hasCall(client, "create") || hasCall(client, "modify "+msbSandbox) {
		t.Fatalf("an instance with the stale port mapping must not be started or changed in place: %v", client.calls)
	}
}

func TestBackendLaunchWaitsForGuestSkillInventory(t *testing.T) {
	for _, status := range []string{"stopped", "running"} {
		t.Run(status, func(t *testing.T) {
			client := &fakeMSBClient{
				exists: true, status: status, startScript: msbStartScript(IsolationBackend),
				execCaptureResults: []fakeMSBExecCaptureResult{{
					stderr: "managed skill directory exists without its project-skill inventory", code: 1,
				}},
			}
			m := newTestMicrosandbox(t, client)
			m.cfg.Isolation = IsolationBackend
			m.cfg.ProjectSkillsManaged = true
			err := m.startInstance(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), "project-skill inventory") {
				t.Fatalf("start error = %v, want orphaned skill inventory refusal", err)
			}
			if hasCall(client, "exec "+msbSandbox+" "+msbRelaunchCommand) {
				t.Fatalf("backend launched before skill inventory refusal: %v", client.calls)
			}
			if status == "stopped" && !hasCall(client, "start "+msbSandbox) {
				t.Fatalf("stopped VM was not booted before checking its inventory: %v", client.calls)
			}
			if status == "running" && hasCall(client, "start "+msbSandbox) {
				t.Fatalf("running VM was unexpectedly started again: %v", client.calls)
			}
		})
	}
}

func TestStoppedBackendLaunchesAfterGuestSkillInventoryCheck(t *testing.T) {
	client := &fakeMSBClient{
		exists: true, status: "stopped", startScript: msbStartScript(IsolationBackend),
		execCaptureResults: []fakeMSBExecCaptureResult{{stdout: `{"schemaVersion":1,"skills":[]}`}},
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationBackend
	m.cfg.ProjectSkillsManaged = true
	if err := m.startInstance(context.Background(), nil); err != nil {
		t.Fatalf("startInstance: %v", err)
	}
	startAt, inventoryAt, launchAt := -1, -1, -1
	for i, call := range client.calls {
		switch {
		case call == "start "+msbSandbox:
			startAt = i
		case strings.HasPrefix(call, "execcapture "+msbSandbox):
			inventoryAt = i
		case call == "exec "+msbSandbox+" "+msbRelaunchCommand:
			launchAt = i
		}
	}
	if startAt < 0 || inventoryAt <= startAt || launchAt <= inventoryAt {
		t.Fatalf("VM boot, inventory validation, and backend launch must occur in order: %v", client.calls)
	}
}

func TestMicrosandboxRestartRejectsServerPortBeforeStopping(t *testing.T) {
	client := &fakeMSBClient{
		exists:              true,
		status:              "running",
		startScript:         msbStartScript(IsolationFull),
		serverPortForwarded: true,
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	err := m.Restart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "4096") || !strings.Contains(err.Error(), "recreate --microsandbox") {
		t.Fatalf("Restart error = %v, want an explicit recreation refusal for port 4096", err)
	}
	if hasCall(client, "stop "+msbSandbox) || hasCall(client, "start "+msbSandbox) {
		t.Fatalf("restart must refuse the stale port map before stopping the existing guest: %v", client.calls)
	}
}

// A running full-mode sandbox where nothing is preparing (an idle VM left by
// the pre-#61 creation path, or a start script whose launch retries were
// exhausted) must be relaunched rather than polled: no process will ever
// write the marker on its own.
func TestMicrosandboxFullModeIdleGuestIsRelaunched(t *testing.T) {
	client := &fakeMSBClient{
		exists:      true,
		status:      "running",
		startScript: msbStartScript(IsolationFull),
		execResults: []fakeMSBExecResult{
			{code: msbPrepareIdle},  // nothing preparing
			{code: 0},               // relaunch
			{code: msbPrepareReady}, // prepared
		},
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.toolchainPollDelay = time.Millisecond
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !hasCall(client, "exec "+msbSandbox+" "+msbRelaunchCommand) {
		t.Fatalf("an idle guest must be relaunched: %v", client.calls)
	}
}

// The relaunch loop is bounded: a guest that stays idle cannot spin.
func TestMicrosandboxFullModeIdleRelaunchIsBounded(t *testing.T) {
	client := &fakeMSBClient{
		exists:      true,
		status:      "running",
		startScript: msbStartScript(IsolationFull),
		execResults: []fakeMSBExecResult{
			{code: msbPrepareIdle}, {code: 0},
			{code: msbPrepareIdle}, {code: 0},
			{code: msbPrepareIdle}, {code: 0},
		},
		execDefault: &fakeMSBExecResult{code: msbPrepareIdle},
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.cfg.StartTimeout = 60 * time.Millisecond
	m.toolchainPollDelay = time.Millisecond
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("Start must fail when the guest never becomes ready")
	}
	relaunches := 0
	for _, c := range client.calls {
		if c == "exec "+msbSandbox+" "+msbRelaunchCommand {
			relaunches++
		}
	}
	if relaunches != msbToolchainRelaunchLimit {
		t.Fatalf("relaunches = %d, want the limit %d; calls: %v", relaunches, msbToolchainRelaunchLimit, client.calls)
	}
}

// An invalid JUST_CODE_START_TIMEOUT must surface as a configuration error in
// full mode too: attach never reaches the validation the backend path does.
func TestMicrosandboxFullModeSurfacesInvalidStartTimeout(t *testing.T) {
	client := &fakeMSBClient{execDefault: &fakeMSBExecResult{code: msbPrepareProgress}}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.cfg.StartTimeoutErr = errors.New("JUST_CODE_START_TIMEOUT must be a whole number of seconds")
	err := m.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "JUST_CODE_START_TIMEOUT") {
		t.Fatalf("Start error = %v, want the configuration error", err)
	}
}

// A full-mode sandbox stopped mid-preparation never finishes on boot (the
// entrypoint only runs at creation): the stopped path relaunches the
// idempotent start script and gates on the marker.
func TestMicrosandboxFullModeStoppedRelaunchesAndWaits(t *testing.T) {
	client := &fakeMSBClient{
		exists:      true,
		status:      "stopped",
		startScript: msbStartScript(IsolationFull),
		execResults: []fakeMSBExecResult{
			{code: 0},                  // relaunch
			{code: msbPrepareProgress}, // installer alive
			{code: msbPrepareReady},    // prepared
		},
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	m.toolchainPollDelay = time.Millisecond
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !hasCall(client, "exec "+msbSandbox+" "+msbRelaunchCommand) {
		t.Fatalf("stopped full-mode sandbox must relaunch the start script: %v", client.calls)
	}
}

func TestMicrosandboxDoctorReportsDiagnostics(t *testing.T) {
	client := &fakeMSBClient{doctorOutput: "KVM: available\n"}
	m := newTestMicrosandbox(t, client)
	out := captureStdout(t, func() {
		if err := m.Doctor(context.Background()); err != nil {
			t.Fatalf("Doctor: %v", err)
		}
	})
	if !strings.Contains(out, "KVM: available") || !strings.Contains(out, "is ready") {
		t.Fatalf("doctor output = %q", out)
	}
	if hasCall(client, "ensure") {
		t.Fatalf("Doctor installed the runtime: %v", client.calls)
	}
	if !hasCall(client, "doctor") {
		t.Fatalf("Doctor did not inspect the installed runtime: %v", client.calls)
	}
}

func TestMicrosandboxDoctorSurfacesFailure(t *testing.T) {
	client := &fakeMSBClient{doctorErr: errors.New("KVM device missing")}
	m := newTestMicrosandbox(t, client)
	err := m.Doctor(context.Background())
	if err == nil || !strings.Contains(err.Error(), "KVM device missing") {
		t.Fatalf("Doctor error = %v", err)
	}
}

func TestSDKMicrosandboxDoctorDoesNotInstallMissingRuntime(t *testing.T) {
	home := t.TempDir()
	runtimePath := filepath.Join(home, "bin", "msb")
	t.Setenv("MSB_HOME", home)
	t.Setenv("MSB_PATH", "")
	t.Setenv("MSB_LIBKRUNFW_PATH", "")

	_, err := (sdkMSBClient{}).Doctor(context.Background())
	if err == nil || !strings.Contains(err.Error(), "managed Microsandbox runtime is missing") {
		t.Fatalf("Doctor error = %v, want missing-runtime diagnostic", err)
	}
	if _, statErr := os.Stat(runtimePath); !os.IsNotExist(statErr) {
		t.Fatalf("Doctor created runtime path: stat error = %v", statErr)
	}
	entries, readErr := os.ReadDir(home)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("Doctor modified runtime home: entries = %v, error = %v", entries, readErr)
	}
}

func TestMicrosandboxIsRunning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		exists bool
		status string
		want   bool
	}{
		{"running", true, "running", true},
		{"stopped", true, "stopped", false},
		{"absent", false, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestMicrosandbox(t, &fakeMSBClient{exists: tc.exists, status: tc.status})
			got, err := m.IsRunning(context.Background())
			if err != nil || got != tc.want {
				t.Fatalf("IsRunning = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

// TestMicrosandboxRefusesLegacyBindMount pins the P22 provenance rule: an
// instance whose /workspace is a host bind mount predates the sealed model
// and exposes the host checkout to the guest. It is refused, never converted
// in place — a warning would leave the leak running.
func TestMicrosandboxRefusesLegacyBindMount(t *testing.T) {
	client := &fakeMSBClient{exists: true, status: "running", mount: "/somewhere/else"}
	m := newTestMicrosandbox(t, client)
	err := m.Start(context.Background())
	if err == nil {
		t.Fatal("Start must refuse an instance with a host bind mount at /workspace")
	}
	for _, want := range []string{"/somewhere/else", "clean --microsandbox", "never converted in place"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must mention %q: %v", want, err)
		}
	}
}

// TestMicrosandboxRefusesUnreadableWorkspaceProvenance pins the fail-closed
// half: when the persisted configuration cannot be read, the runtime cannot
// prove the workspace is sealed, so it must not boot the instance.
func TestMicrosandboxRefusesUnreadableWorkspaceProvenance(t *testing.T) {
	client := &fakeMSBClient{exists: true, status: "running", mountErr: fmt.Errorf("config unreadable")}
	m := newTestMicrosandbox(t, client)
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("Start must fail closed when the workspace provenance cannot be read")
	}
}

func TestMicrosandboxStopAndClean(t *testing.T) {
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := m.Clean(context.Background()); err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if !hasCall(client, "stop "+msbSandbox) || !hasCall(client, "remove "+msbSandbox) {
		t.Fatalf("lifecycle calls = %v", client.calls)
	}
}

func TestMicrosandboxDelegatesInteractiveCommands(t *testing.T) {
	client := &fakeMSBClient{}
	m := newTestMicrosandbox(t, client)
	if err := m.Logs(); err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if err := m.Shell(); err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if !hasCall(client, "logs "+msbSandbox) || !hasCall(client, "shell "+msbSandbox) {
		t.Fatalf("interactive calls = %v", client.calls)
	}
}

func TestMSBCreateOptions(t *testing.T) {
	spec := msbSandboxSpec{
		Image:           msbImage,
		Isolation:       IsolationBackend,
		Env:             map[string]string{"OPENCODE_SERVER_USERNAME": "opencode"},
		SealedWorkspace: true,
		Bindings:        bindingsMetadata(testBindings("secret-value")),
		StartScript:     "exec opencode serve",
	}
	var cfg msb.SandboxConfig
	for _, option := range msbCreateOptions(spec) {
		option(&cfg)
	}
	if cfg.Image != msbImage || cfg.CPUs != 2 || cfg.MemoryMiB != 4096 || !cfg.Detached {
		t.Fatalf("resource config = %+v", cfg)
	}
	if cfg.Workdir != "/workspace" || cfg.Shell != "/bin/sh" || !reflect.DeepEqual(cfg.Entrypoint, []string{"/bin/sh", "-c"}) {
		t.Fatalf("process config = %+v", cfg)
	}
	if cfg.RootDisk == nil || cfg.RootDisk.SizeMiB != 8*1024 {
		t.Fatalf("root disk = %+v", cfg.RootDisk)
	}
	// P22: /workspace is owned storage inside the sandbox. There is no host
	// path in the mount, so no host file is reachable from the guest.
	ws := cfg.Volumes["/workspace"]
	if ws.Bind != "" {
		t.Fatalf("the workspace must not be a host bind mount: %+v", ws)
	}
	if ws.Kind() != msb.MountKindOwned {
		t.Fatalf("workspace mount kind = %v, want Owned", ws.Kind())
	}
	for port := uint16(3000); port <= 3010; port++ {
		if !hasMSBPortBinding(cfg.PortBindings, port, port) {
			t.Fatalf("preview port %d missing: %v", port, cfg.PortBindings)
		}
	}
	if !hasMSBPortBinding(cfg.PortBindings, uint16(DefaultPort), uint16(DefaultPort)) || cfg.Network == nil {
		t.Fatalf("network config = port bindings %v, network %+v", cfg.PortBindings, cfg.Network)
	}
	if len(cfg.Secrets) != 1 || cfg.Secrets[0].EnvVar != msbAPISecretEnv {
		t.Fatalf("secret config = %+v", cfg.Secrets)
	}
	// The create surface only accepts inline values, so the binding is
	// registered with the inert bootstrap sentinel — never the real value —
	// and rotated to the host env reference right after creation.
	if cfg.Secrets[0].Value != msbSecretBootstrapValue {
		t.Fatalf("create must register the bootstrap sentinel, not a value: %+v", cfg.Secrets[0])
	}
	if !reflect.DeepEqual(cfg.Secrets[0].Allow, []string{msbAllowHost}) {
		t.Fatalf("allowed hosts = %v", cfg.Secrets[0].Allow)
	}
	if cfg.Scripts["start"] != spec.StartScript {
		t.Fatalf("start script = %q", cfg.Scripts["start"])
	}
}

// TestMSBFullModeDoesNotPublishOpenCodeServerPort pins the P12 full-mode
// boundary: the TUI runs in the guest, so no OpenCode server port should be
// forwarded to the host. Development preview ports remain available on
// loopback.
func TestMSBFullModeDoesNotPublishOpenCodeServerPort(t *testing.T) {
	spec := msbSandboxSpec{
		Image:           msbImage,
		Isolation:       IsolationFull,
		SealedWorkspace: true,
		StartScript:     msbStartScript(IsolationFull),
	}
	var cfg msb.SandboxConfig
	for _, option := range msbCreateOptions(spec) {
		option(&cfg)
	}
	if hasMSBPortBinding(cfg.PortBindings, uint16(DefaultPort), uint16(DefaultPort)) {
		t.Fatalf("full mode must not forward the OpenCode server port %d: %v", DefaultPort, cfg.PortBindings)
	}
	for port := uint16(3000); port <= 3010; port++ {
		if !hasMSBPortBinding(cfg.PortBindings, port, port) {
			t.Fatalf("preview port %d missing from full-mode config: %v", port, cfg.PortBindings)
		}
	}
}

func hasMSBPortBinding(bindings []msb.PortBinding, host, guest uint16) bool {
	for _, binding := range bindings {
		if binding.Bind == "127.0.0.1" && binding.HostPort == host && binding.GuestPort == guest {
			return true
		}
	}
	return false
}

func TestMSBConfiguredPortsReplacePreviewDefaultsAndStayLoopback(t *testing.T) {
	spec := msbSandboxSpec{
		Image:           msbImage,
		Isolation:       IsolationFull,
		SealedWorkspace: true,
		PortsConfigured: true,
		Ports:           []PortMapping{{Host: 8080, Guest: 80}},
		StartScript:     msbStartScript(IsolationFull),
	}
	var cfg msb.SandboxConfig
	for _, option := range msbCreateOptions(spec) {
		option(&cfg)
	}
	if !hasMSBPortBinding(cfg.PortBindings, 8080, 80) {
		t.Fatalf("configured port binding missing: %+v", cfg.PortBindings)
	}
	if hasMSBPortBinding(cfg.PortBindings, 3000, 3000) {
		t.Fatalf("explicit mapping set must replace default previews: %+v", cfg.PortBindings)
	}
	for _, binding := range cfg.PortBindings {
		if binding.Bind != "127.0.0.1" {
			t.Fatalf("port mapping must be loopback-only: %+v", binding)
		}
	}
}

func TestMSBNextStartOptionsRefreshesSecret(t *testing.T) {
	env := map[string]string{"OPENCODE_SERVER_PASSWORD": "new-password"}
	bindings := bindingsMetadata(testBindings("new-key"))
	options := msbNextStartOptions(env, bindings, nil)
	if options.Policy != msb.ModificationPolicyNextStart || !reflect.DeepEqual(options.Env, env) {
		t.Fatalf("modify options = %+v", options)
	}
	secret := options.Secrets[msbAPISecretEnv]
	// The refresh is a host env *reference*: no value is ever persisted.
	if secret.Value != "" || secret.Env != bindings[0].HostEnv {
		t.Fatalf("updated secret must be an env reference, not a value: %+v", secret)
	}
	if !reflect.DeepEqual(secret.AllowedHosts, []string{msbAllowHost}) {
		t.Fatalf("allowed hosts = %+v", secret)
	}
}

type fakeDetachable struct {
	called bool
	err    error
}

func (f *fakeDetachable) Detach(ctx context.Context) error {
	f.called = true
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("detach context is not bounded")
	}
	return f.err
}

func TestDetachMSBSandbox(t *testing.T) {
	sb := &fakeDetachable{}
	if err := detachMSBSandbox(sb); err != nil {
		t.Fatalf("detachMSBSandbox: %v", err)
	}
	if !sb.called {
		t.Fatal("Detach was not called")
	}
}

func TestMSBRuntimeBinaryUsesManagedHome(t *testing.T) {
	t.Setenv("MSB_HOME", t.TempDir())
	path, err := msbRuntimeBinary()
	if err != nil {
		t.Fatalf("msbRuntimeBinary: %v", err)
	}
	name := "msb"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if !strings.HasSuffix(path, string(os.PathSeparator)+"bin"+string(os.PathSeparator)+name) {
		t.Fatalf("managed runtime path = %q", path)
	}
}

// TestMicrosandboxRestartAdvisesOnDotenvButProceeds pins the P22 change of
// role for the workspace scan: with no host mount, a .env in the checkout is
// host hygiene advice, not a blocked start. The file never reaches the guest
// because the transfer filter refuses it, which is the enforceable boundary.
func TestMicrosandboxRestartAdvisesOnDotenvButProceeds(t *testing.T) {
	client := &fakeMSBClient{exists: true}
	m := newTestMicrosandbox(t, client)
	if err := os.WriteFile(filepath.Join(m.cfg.WorkspaceDir, ".env"), []byte("X=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := captureStderr(t, func() {
		if err := m.Restart(context.Background()); err != nil {
			t.Fatalf("Restart must proceed: a host .env is not mounted and cannot reach the guest: %v", err)
		}
	})
	if !strings.Contains(stderr, ".env") || !strings.Contains(stderr, "excluded from transfer") {
		t.Fatalf("hygiene note must name the excluded file: %q", stderr)
	}
	// And the filter really excludes it: the dotenv file is not in the
	// transferable set.
	man, err := ResolveTransferSet(context.Background(), m.cfg.WorkspaceDir, TransferOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range man.Included() {
		if e.Rel == ".env" {
			t.Fatal("the .env file must not be in the transfer set")
		}
	}
}

func TestMicrosandboxRestartCreatesMissingWorkspace(t *testing.T) {
	// Restart preflights the workspace, but a workspace that does not exist
	// yet (default ./workspace) must be created like Start does, not fail
	// the preflight with a stat error.
	client := &fakeMSBClient{exists: true}
	m := newTestMicrosandbox(t, client)
	m.cfg.WorkspaceDir = filepath.Join(t.TempDir(), "missing", "workspace")
	if err := m.Restart(context.Background()); err != nil {
		t.Fatalf("Restart must create a missing workspace, got: %v", err)
	}
	if info, err := os.Stat(m.cfg.WorkspaceDir); err != nil || !info.IsDir() {
		t.Fatalf("workspace was not created: %v", err)
	}
}

func TestMicrosandboxRestartIsNonDestructive(t *testing.T) {
	// P07: restart stops and starts the existing instance. It must never
	// Remove it — the destructive rebuild is the explicit Recreate.
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	if err := m.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	for _, call := range client.calls {
		if strings.HasPrefix(call, "remove") {
			t.Fatalf("Restart must not Remove the instance: %v", client.calls)
		}
	}
	if !hasCall(client, "stop ") {
		t.Fatalf("Restart must stop the instance: %v", client.calls)
	}
}

func TestMicrosandboxRecreateRebuildsAndDropsJournal(t *testing.T) {
	// Recreate is the explicit destructive path: it removes and re-creates,
	// then records the new creation-fixed configuration with an empty journal.
	client := &fakeMSBClient{exists: true}
	m := newTestMicrosandbox(t, client)
	m.cfg.PortsConfigured = true
	m.cfg.Ports = []PortMapping{{Host: 8080, Guest: 80}}
	if err := m.Recreate(context.Background()); err != nil {
		t.Fatalf("Recreate: %v", err)
	}
	if !hasCall(client, "remove") {
		t.Fatalf("Recreate must remove the instance: %v", client.calls)
	}
	state, err := ReadInstanceState(DefaultFS, instanceStatePath(DefaultStateDir(), m.InstanceName()))
	if err != nil {
		t.Fatalf("read recreated state: %v", err)
	}
	if state == nil || !state.PortsConfigured || !equalPortMappings(state.Ports, m.cfg.Ports) || len(state.Pending) != 0 {
		t.Fatalf("recreated instance state = %+v, want configured ports and empty journal", state)
	}
}

func TestMicrosandboxRestartRejectsBadConfigBeforeClean(t *testing.T) {
	// A configuration error (missing API key) must abort restart before
	// the destructive Clean: the sandbox and its persistent state survive,
	// instead of being removed and then failing to recreate in Start.
	client := &fakeMSBClient{exists: true}
	m := newTestMicrosandbox(t, client)
	m.cfg.APIKey = ""
	// The credential store is not what this test exercises: inject the
	// resolution seam so a missing key is reported as missing, not as a
	// store-unreachable error on hosts without a native store.
	m.credentialRead = func(context.Context, CredentialKind, string) (string, string, error) {
		return "", "", ErrCredentialNotFound
	}
	if err := m.Restart(context.Background()); err == nil {
		t.Fatal("Restart must refuse a missing ALBERT_API_KEY")
	} else if !strings.Contains(err.Error(), "ALBERT_API_KEY") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(client.calls) != 0 {
		t.Fatalf("client call ran despite the configuration error: %v", client.calls)
	}
}

// TestMicrosandboxFullModeSpecUsesProxySecret pins the credential contract for
// isolation full: the TUI runs inside the microVM, but that must not move the
// real key into the guest. The secret travels through the runtime's network
// proxy, exactly as in backend mode, and the guest only ever sees the
// placeholder Microsandbox exposes under ALBERT_API_KEY.
func TestMicrosandboxFullModeSpecUsesProxySecret(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.cfg.Isolation = IsolationFull
	spec := m.sandboxSpec(testBindings(m.cfg.APIKey))
	if len(spec.Bindings) != 1 || spec.Bindings[0].GuestEnv != msbAPISecretEnv {
		t.Fatalf("full mode must configure the proxy binding: %+v", spec)
	}
	if !reflect.DeepEqual(spec.Bindings[0].AllowHosts, []string{msbAllowHost}) {
		t.Fatalf("full mode must restrict proxy hosts to Albert: %v", spec.Bindings[0].AllowHosts)
	}
	if got := spec.Env[msbAPISecretEnv]; got != "" {
		t.Fatalf("full mode guest env must not carry the real key: %v", spec.Env)
	}
	if spec.Env["OPENCODE_CONFIG_CONTENT"] == "" {
		t.Fatal("full mode guest env must carry the OpenCode config")
	}
	if !strings.Contains(spec.StartScript, "sleep infinity") || strings.Contains(spec.StartScript, "opencode serve") {
		t.Fatalf("full mode start script must keep the VM alive, not serve: %q", spec.StartScript)
	}
	if !strings.Contains(spec.StartScript, "apk add") {
		t.Fatalf("full mode start script must keep the shared toolchain prep: %q", spec.StartScript)
	}
}

func TestMCPOverlayPropagatesToInitialAndNextStartGuestConfig(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.OpenCodeOverlay.MCPConnectors = []string{"data-gouv"}
	for name, content := range map[string]string{
		"initial":    m.sandboxEnv()["OPENCODE_CONFIG_CONTENT"],
		"next start": m.nextStartEnv()["OPENCODE_CONFIG_CONTENT"],
	} {
		var config struct {
			MCP map[string]struct {
				Type    string `json:"type"`
				URL     string `json:"url"`
				Enabled bool   `json:"enabled"`
			} `json:"mcp"`
		}
		if err := json.Unmarshal([]byte(content), &config); err != nil {
			t.Fatalf("%s config: %v", name, err)
		}
		connector, ok := config.MCP["data-gouv"]
		if !ok || connector.Type != "remote" || connector.URL != "https://mcp.data.gouv.fr/mcp" || !connector.Enabled {
			t.Fatalf("%s MCP config = %+v", name, config.MCP)
		}
	}
}

// TestMicrosandboxSpecNeverLeaksRealKey is the regression guard for the whole
// change: whatever the isolation level, no sandbox spec may place the host
// secret in the guest environment.
func TestMicrosandboxSpecNeverLeaksRealKey(t *testing.T) {
	for _, isolation := range []Isolation{IsolationBackend, IsolationFull, ""} {
		m := newTestMicrosandbox(t, &fakeMSBClient{})
		m.cfg.Isolation = isolation
		spec := m.sandboxSpec(testBindings(m.cfg.APIKey))
		for key, value := range spec.Env {
			if value == m.cfg.APIKey {
				t.Fatalf("isolation %q leaks the real key into guest env %q", isolation, key)
			}
		}
		for key, value := range m.nextStartEnv() {
			if value == m.cfg.APIKey {
				t.Fatalf("isolation %q leaks the real key into next-start env %q", isolation, key)
			}
		}
	}
}

func TestMicrosandboxBackendModeSpecUnchanged(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	spec := m.sandboxSpec(testBindings(m.cfg.APIKey))
	if len(spec.Bindings) != 1 || !reflect.DeepEqual(spec.Bindings[0].AllowHosts, []string{msbAllowHost}) {
		t.Fatalf("backend mode must keep the proxy binding: %+v", spec)
	}
	if spec.Env[msbAPISecretEnv] != "" {
		t.Fatalf("backend mode must not leak the real key into guest env: %v", spec.Env)
	}
	if !strings.Contains(spec.StartScript, "exec opencode serve") {
		t.Fatalf("backend mode start script must serve: %q", spec.StartScript)
	}
}

// TestMicrosandboxFullModeNextStartUsesProxySecretAndScrubsRawEnv covers the
// stopped-sandbox path. The proxy secret must be refreshed for the next boot
// just as in backend mode, and any plaintext ALBERT_API_KEY persisted by an
// earlier version must be removed from the guest environment in the same
// modification.
func TestMicrosandboxFullModeNextStartUsesProxySecretAndScrubsRawEnv(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{exists: true, status: "stopped"})
	m.cfg.Isolation = IsolationFull
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if m.cfg.Isolation != IsolationFull {
		t.Fatal("config lost the isolation level")
	}
	client := m.Client.(*fakeMSBClient)
	if len(client.modifiedBindings) != 1 || client.modifiedBindings[0].GuestEnv != msbAPISecretEnv {
		t.Fatalf("full mode must refresh the proxy binding on restart: %+v", client.modifiedBindings)
	}
	if client.modifiedEnv[msbAPISecretEnv] != "" {
		t.Fatalf("full mode next-start env must not carry the real key: %v", client.modifiedEnv)
	}
	bindings := bindingsMetadata(testBindings(m.cfg.APIKey))
	options := msbNextStartOptions(m.nextStartEnv(), bindings, nil)
	if !containsString(options.EnvRemove, msbAPISecretEnv) {
		t.Fatalf("next-start must remove a persisted plaintext key: %v", options.EnvRemove)
	}
	secret := options.Secrets[msbAPISecretEnv]
	if secret.Value != "" || secret.Env != bindings[0].HostEnv || !reflect.DeepEqual(secret.AllowedHosts, []string{msbAllowHost}) {
		t.Fatalf("proxy secret not refreshed as an env reference: %+v", secret)
	}
	for _, prefix := range []string{"modify " + msbSandbox, "start " + msbSandbox} {
		if !hasCall(client, prefix) {
			t.Fatalf("missing %q; calls: %v", prefix, client.calls)
		}
	}
	// Booting a stopped VM does not re-run the entrypoint, so the idempotent
	// start script is relaunched (a sandbox stopped mid-preparation would
	// otherwise never finish installing) and readiness is gated on the
	// toolchain marker.
	if !hasCall(client, "exec "+msbSandbox+" "+msbRelaunchCommand) {
		t.Fatalf("full mode restart must relaunch the start script: %v", client.calls)
	}
	if !hasCall(client, "exec "+msbSandbox+" "+msbGuestPrepareProbe) {
		t.Fatalf("readiness marker was not checked: %v", client.calls)
	}
}

// TestMicrosandboxFullModeRejectsLegacyRawKey pins the migration guard: a
// sandbox whose persisted guest environment holds a real key cannot be fixed
// in place, so the start must fail with the recreate command instead of
// booting a VM that keeps serving a plaintext credential.
func TestMicrosandboxFullModeRejectsLegacyRawKey(t *testing.T) {
	client := &fakeMSBClient{
		exists:      true,
		status:      "stopped",
		startScript: msbStartScript(IsolationFull),
		env:         map[string]string{"ALBERT_API_KEY": "real-persisted-key"},
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	err := m.Start(context.Background())
	if err == nil {
		t.Fatal("a sandbox persisting a plaintext key must not boot")
	}
	if !strings.Contains(err.Error(), "restart --microsandbox") {
		t.Fatalf("error must point at the recreate command: %v", err)
	}
	if hasCall(client, "start "+msbSandbox) {
		t.Fatalf("a legacy sandbox must not be started: %v", client.calls)
	}
}

// TestMicrosandboxFullModeAcceptsPlaceholderEnv records that the guard targets
// the plaintext value, not the variable name: a sandbox already using the
// proxy stores the placeholder there and starts normally.
func TestMicrosandboxFullModeAcceptsPlaceholderEnv(t *testing.T) {
	client := &fakeMSBClient{
		exists:      true,
		status:      "stopped",
		startScript: msbStartScript(IsolationFull),
		env:         map[string]string{"ALBERT_API_KEY": "$MSB_ALBERT_API_KEY"},
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !hasCall(client, "start "+msbSandbox) {
		t.Fatalf("a proxied sandbox must start: %v", client.calls)
	}
}

// TestMicrosandboxStartFailsClosedOnUnreadableEnv pins the refusal when the
// guest environment cannot be inspected. Scrub-on-next-start never runs for a
// running sandbox, and a stopped one cannot be verified either, so an
// unreadable config is not evidence of a protected sandbox: booting on that
// assumption could keep serving a plaintext key.
func TestMicrosandboxStartFailsClosedOnUnreadableEnv(t *testing.T) {
	client := &fakeMSBClient{
		exists:      true,
		status:      "stopped",
		startScript: msbStartScript(IsolationFull),
		envErr:      errors.New("config unavailable"),
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	err := m.Start(context.Background())
	if err == nil {
		t.Fatal("an unreadable guest environment must not boot")
	}
	if !strings.Contains(err.Error(), "restart --microsandbox") {
		t.Fatalf("error must point at the recreate command: %v", err)
	}
	if !errors.Is(err, client.envErr) {
		t.Fatalf("error must carry the inspection failure: %v", err)
	}
	if hasCall(client, "start "+msbSandbox) {
		t.Fatalf("an unverifiable sandbox must not be started: %v", client.calls)
	}
}

// persistedConfigWithGuestEnv renders the JSON the runtime stores for a
// sandbox whose spec environment holds the given entries.
//
// The env field is an array of {key,value} objects, not a JSON object: the
// runtime flattens its spec into the sandbox config (serde flatten,
// sdk/rust/lib/sandbox/config.rs) and the spec's env is a Vec<EnvVar> where
// EnvVar is {key, value} (packages/microsandbox-types/rust/lib/domain.rs).
// Reproducing that shape here is the point of these tests: a fixture shaped
// like the map the SDK would need would pass while production saw nothing.
func persistedConfigWithGuestEnv(entries ...string) string {
	return `{"name":"` + msbSandbox + `",` +
		`"runtime":{"workdir":"/workspace","shell":"/bin/sh","scripts":{"start":"exec sleep infinity"}},` +
		`"env":[` + strings.Join(entries, ",") + `]}`
}

// persistedGuestEnvFromConfig runs the production reader over the persisted
// fixture, so the guard tests below start from the stored JSON rather than from
// a hand-built map the runtime could never produce.
func persistedGuestEnvFromConfig(t *testing.T, entries ...string) map[string]string {
	t.Helper()
	env, err := parseGuestEnv(persistedConfigWithGuestEnv(entries...))
	if err != nil {
		t.Fatalf("parseGuestEnv: %v", err)
	}
	return env
}

// TestMicrosandboxLegacyGuardReadsThePersistedConfig is the end-to-end check of
// the guard against the stored shape, and the test that fails when the reader
// looks at a field the SDK never fills: a legacy sandbox persisting the
// plaintext key must be refused, and one persisting the placeholder must boot.
func TestMicrosandboxLegacyGuardReadsThePersistedConfig(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		wantErr bool
	}{
		{
			name:    "pre-proxy sandbox holds the plaintext key",
			entries: []string{`{"key":"OPENCODE_SERVER_USERNAME","value":"opencode"}`, `{"key":"` + msbAPISecretEnv + `","value":"albert-real-secret"}`},
			wantErr: true,
		},
		{
			name:    "proxied sandbox holds the placeholder",
			entries: []string{`{"key":"` + msbAPISecretEnv + `","value":"` + msbAPISecretPlaceholder + `"}`},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &fakeMSBClient{
				exists:      true,
				status:      "stopped",
				startScript: msbStartScript(IsolationFull),
				env:         persistedGuestEnvFromConfig(t, tt.entries...),
			}
			m := newTestMicrosandbox(t, client)
			m.cfg.Isolation = IsolationFull
			err := m.Start(context.Background())
			if tt.wantErr {
				if err == nil {
					t.Fatal("a sandbox persisting a plaintext key must not boot")
				}
				if !strings.Contains(err.Error(), "restart --microsandbox") {
					t.Fatalf("error must point at the recreate command: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if !hasCall(client, "start "+msbSandbox) {
				t.Fatalf("the sandbox must start: %v", client.calls)
			}
		})
	}
}

// TestParseGuestEnvReadsThePersistedEnvShape pins the reader against the shape
// the runtime actually persists. The legacy guard is only worth anything if it
// sees the stored value, so this asserts the array form directly.
func TestParseGuestEnvReadsThePersistedEnvShape(t *testing.T) {
	config := persistedConfigWithGuestEnv(
		`{"key":"OPENCODE_SERVER_USERNAME","value":"opencode"}`,
		`{"key":"`+msbAPISecretEnv+`","value":"albert-real-secret"}`,
	)
	env, err := parseGuestEnv(config)
	if err != nil {
		t.Fatalf("parseGuestEnv: %v", err)
	}
	if got := env[msbAPISecretEnv]; got != "albert-real-secret" {
		t.Fatalf("%s = %q, want the persisted value", msbAPISecretEnv, got)
	}
	if got := env["OPENCODE_SERVER_USERNAME"]; got != "opencode" {
		t.Fatalf("OPENCODE_SERVER_USERNAME = %q, want %q", got, "opencode")
	}
}

// TestParseGuestEnvIgnoresAbsentAndSecretsSections keeps the reader narrow: a
// sandbox with no spec env yields an empty environment rather than an error, so
// callers can tell "nothing persisted" from "could not read".
func TestParseGuestEnvIgnoresAbsentAndSecretsSections(t *testing.T) {
	env, err := parseGuestEnv(`{"name":"` + msbSandbox + `"}`)
	if err != nil {
		t.Fatalf("parseGuestEnv without an env section: %v", err)
	}
	if len(env) != 0 {
		t.Fatalf("env = %v, want empty", env)
	}
	if _, err := parseGuestEnv(""); err == nil {
		t.Fatal("an unreadable config must surface an error, not an empty environment")
	}
}

// TestSDKSandboxConfigCannotSeeThePersistedGuestEnv is the regression guard for
// the reason parseGuestEnv exists at all. The SDK's typed SandboxConfig decodes
// the stored config into an internal struct with no top-level env field, and
// fills the public Env map only from the init section, so cfg.Env stays nil for
// anything created from a spec env. A guard reading cfg.Env therefore never
// fires. If a future SDK version starts populating it, this fails and the raw
// parse can be revisited.
func TestSDKSandboxConfigCannotSeeThePersistedGuestEnv(t *testing.T) {
	config := persistedConfigWithGuestEnv(
		`{"key":"` + msbAPISecretEnv + `","value":"albert-real-secret"}`,
	)
	var cfg msb.SandboxConfig
	if err := json.Unmarshal([]byte(config), &cfg); err != nil {
		t.Fatalf("unmarshal SandboxConfig: %v", err)
	}
	if _, ok := cfg.Env[msbAPISecretEnv]; ok {
		t.Fatalf("SandboxConfig.Env now exposes the spec env (%v); the raw parse may be unnecessary", cfg.Env)
	}
}

// persistedConfigWithMounts reproduces the mounts section of a real persisted
// config document (captured from a live albert-opencode-sandbox created by
// just-code 0.4.1, runtime v0.7.0). Keeping the full entry shape — options,
// stat_virtualization, follow_root_symlinks — matters: a fixture trimmed to
// what parseWorkspaceMount reads would not prove the parser accepts what the
// runtime actually writes.
func persistedConfigWithMounts(mounts ...string) string {
	return `{"name":"` + msbSandbox + `",` +
		`"runtime":{"workdir":"/workspace","shell":"/bin/sh"},` +
		`"mounts":[` + strings.Join(mounts, ",") + `]}`
}

const persistedWorkspaceBindMount = `{"type":"Bind","host":"/Users/tester/project","guest":"/workspace",` +
	`"options":{"readonly":false,"noexec":false,"nosuid":false,"nodev":false},` +
	`"stat_virtualization":"strict","host_permissions":"private","follow_root_symlinks":false,"quota_mib":null}`

func TestParseWorkspaceMount(t *testing.T) {
	host, err := parseWorkspaceMount(persistedConfigWithMounts(persistedWorkspaceBindMount), "/workspace")
	if err != nil {
		t.Fatalf("parseWorkspaceMount: %v", err)
	}
	if host != "/Users/tester/project" {
		t.Fatalf("host = %q, want /Users/tester/project", host)
	}

	otherGuest := `{"type":"Bind","host":"/elsewhere","guest":"/data"}`
	host, err = parseWorkspaceMount(persistedConfigWithMounts(otherGuest), "/workspace")
	if err != nil {
		t.Fatalf("parseWorkspaceMount without a /workspace mount: %v", err)
	}
	if host != "" {
		t.Fatalf("host = %q, want empty when no /workspace mount exists", host)
	}

	if _, err := parseWorkspaceMount("", "/workspace"); err == nil {
		t.Fatal("an unreadable config must surface an error, not an empty mount")
	}
}

// TestSDKSandboxConfigCannotSeeThePersistedMounts is the regression guard for
// the reason parseWorkspaceMount exists at all — the mounts twin of
// TestSDKSandboxConfigCannotSeeThePersistedGuestEnv. The SDK's typed
// SandboxConfig decodes Volumes as nil for a sandbox created from spec mounts,
// so a stale-mount check reading cfg.Volumes never fires. If a future SDK
// version starts populating it, this fails and the raw parse can be revisited.
func TestSDKSandboxConfigCannotSeeThePersistedMounts(t *testing.T) {
	config := persistedConfigWithMounts(persistedWorkspaceBindMount)
	var cfg msb.SandboxConfig
	if err := json.Unmarshal([]byte(config), &cfg); err != nil {
		t.Fatalf("unmarshal SandboxConfig: %v", err)
	}
	if len(cfg.Volumes) != 0 {
		t.Fatalf("SandboxConfig.Volumes now exposes the spec mounts (%v); the raw parse may be unnecessary", cfg.Volumes)
	}
}

// TestMicrosandboxFullModeRejectsEnvThatMerelyLooksLikeAPlaceholder covers the
// prefix trap: only the exact documented placeholder is protected, so a value
// that starts with the runtime's placeholder prefix is still treated as a
// credential the guest can read.
func TestMicrosandboxFullModeRejectsEnvThatMerelyLooksLikeAPlaceholder(t *testing.T) {
	for _, value := range []string{"$MSB_OTHER_VAR", "$MSB_", "$MSB_ALBERT_API_KEYx"} {
		client := &fakeMSBClient{
			exists:      true,
			status:      "stopped",
			startScript: msbStartScript(IsolationFull),
			env:         map[string]string{msbAPISecretEnv: value},
		}
		m := newTestMicrosandbox(t, client)
		m.cfg.Isolation = IsolationFull
		if err := m.Start(context.Background()); err == nil {
			t.Fatalf("%q must not pass as the protected placeholder", value)
		}
		if hasCall(client, "start "+msbSandbox) {
			t.Fatalf("%q must not boot: %v", value, client.calls)
		}
	}
}

// TestMicrosandboxFullModeStartsWithNoPersistedKey records that a sandbox whose
// spec env carries no Albert entry at all is not a legacy sandbox: the guard
// looks for a persisted value, not for the variable name.
func TestMicrosandboxFullModeStartsWithNoPersistedKey(t *testing.T) {
	client := &fakeMSBClient{
		exists:      true,
		status:      "stopped",
		startScript: msbStartScript(IsolationFull),
		env:         map[string]string{"OPENCODE_SERVER_USERNAME": "opencode"},
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !hasCall(client, "start "+msbSandbox) {
		t.Fatalf("a sandbox without a persisted key must start: %v", client.calls)
	}
}

// handledConfig is a persisted document exposed the way a real SDK handle
// exposes it. The fake carries no typed config, which is the faithful shape:
// the real handle's typed SandboxConfig cannot see the spec env either, so a
// reader that reaches for it observes nothing.
type handledConfig struct{ configJSON string }

func (h handledConfig) ConfigJSON() string { return h.configJSON }

// TestGuestEnvFromHandleReadsThePersistedDocument covers the wiring, not just
// the parser. The guard is only as good as the source it reads, and the source
// that looks obvious, the handle's typed sandbox config, is blind to the spec
// env. A reader wired to that field no-ops in production while every
// fakeMSBClient test still passes, so this pins the document the reader uses.
func TestGuestEnvFromHandleReadsThePersistedDocument(t *testing.T) {
	h := handledConfig{configJSON: persistedConfigWithGuestEnv(
		`{"key":"` + msbAPISecretEnv + `","value":"albert-real-secret"}`,
	)}
	env, err := guestEnvFromHandle(h)
	if err != nil {
		t.Fatalf("guestEnvFromHandle: %v", err)
	}
	if got := env[msbAPISecretEnv]; got != "albert-real-secret" {
		t.Fatalf("%s = %q, want the value stored in the persisted document", msbAPISecretEnv, got)
	}
}

// persistedConfigWithSecrets builds a persisted sandbox document carrying the
// given secret registrations the way the runtime actually saves them: under
// network.secrets.secrets, not at the top level. The source field is the part
// recovery depends on: only env-reference secrets (source.kind == "env") name a
// host transport variable to republish.
func persistedConfigWithSecrets(secrets ...string) string {
	return `{"name":"` + msbSandbox + `",` +
		`"runtime":{"workdir":"/workspace","shell":"/bin/sh","scripts":{"start":"exec sleep infinity"}},` +
		`"network":{"secrets":{"secrets":[` + strings.Join(secrets, ",") + `]}}}`
}

// TestParsePersistedEnvRefSecretsReadsThePersistedDocument pins what recovery
// reads from the stored document. The typed SandboxConfig never fills its
// Secrets field, so this is the only source that carries the host transport
// variables the stop-start script actually references.
func TestParsePersistedEnvRefSecretsReadsThePersistedDocument(t *testing.T) {
	h := handledConfig{configJSON: persistedConfigWithSecrets(
		`{"name":"ALBERT_API_KEY","source":{"kind":"env","var":"JUST_CODE_HOST_ALBERT_API_KEY"}}`,
	)}
	refs, err := persistedSecretRefsFromHandle(h)
	if err != nil {
		t.Fatalf("persistedSecretRefsFromHandle: %v", err)
	}
	if len(refs) != 1 || refs[0].HostEnv != "JUST_CODE_HOST_ALBERT_API_KEY" {
		t.Fatalf("persisted secret refs = %v, want [JUST_CODE_HOST_ALBERT_API_KEY]", refs)
	}
}

func TestParsePersistedEnvRefSecretsSkipsNonEnvAndDedupes(t *testing.T) {
	config := persistedConfigWithSecrets(
		`{"name":"ALBERT_API_KEY","source":{"kind":"env","var":"JUST_CODE_HOST_ALBERT_API_KEY"}}`,
		`{"name":"ALBERT_API_KEY","source":{"kind":"env","var":"JUST_CODE_HOST_ALBERT_API_KEY"}}`,
		`{"name":"INLINE","value":"not-a-source"}`,
	)
	refs, err := parsePersistedEnvRefSecrets(config)
	if err != nil {
		t.Fatalf("parsePersistedEnvRefSecrets: %v", err)
	}
	if len(refs) != 1 || refs[0].HostEnv != "JUST_CODE_HOST_ALBERT_API_KEY" {
		t.Fatalf("persisted secret refs = %v, want the env reference deduplicated once", refs)
	}
}

// TestParsePersistedEnvRefSecretsEmptyDocument verifies a document with no
// env-reference secrets is an empty result, not an error: that is the signal
// recovery uses to fall back to the current provider configuration.
func TestParsePersistedEnvRefSecretsEmptyDocument(t *testing.T) {
	refs, err := parsePersistedEnvRefSecrets(persistedConfigWithGuestEnv())
	if err != nil {
		t.Fatalf("parsePersistedEnvRefSecrets: %v", err)
	}
	if len(refs) != 0 {
		t.Fatalf("persisted secret refs = %v, want empty", refs)
	}
}

// TestParsePersistedEnvRefSecretsPreservesMetadata pins that recovery keeps the
// destination allowlist and placeholder a project-secret registration persisted
// at creation. These are what prove the registration is still compatible with
// the current approval before recovery republishes the transport variable.
func TestParsePersistedEnvRefSecretsPreservesMetadata(t *testing.T) {
	config := persistedConfigWithSecrets(
		`{"env_var":"JC_API_KEY","source":{"kind":"env","var":"JUST_CODE_HOST_PROJECT_JC_API_KEY"},"allowed_hosts":[{"exact":"api.example.com"}],"placeholder":"__MKR__"}`,
	)
	refs, err := parsePersistedEnvRefSecrets(config)
	if err != nil {
		t.Fatalf("parsePersistedEnvRefSecrets: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("persisted secret refs = %v, want one project-secret registration", refs)
	}
	ref := refs[0]
	if ref.HostEnv != "JUST_CODE_HOST_PROJECT_JC_API_KEY" {
		t.Fatalf("HostEnv = %q, want the project transport variable", ref.HostEnv)
	}
	if len(ref.AllowedHosts) != 1 || ref.AllowedHosts[0] != "api.example.com" {
		t.Fatalf("AllowedHosts = %v, want [api.example.com]", ref.AllowedHosts)
	}
	if ref.Placeholder == nil || *ref.Placeholder != "__MKR__" {
		t.Fatalf("Placeholder = %v, want %q", ref.Placeholder, "__MKR__")
	}
}

// TestParsePersistedEnvRefSecretsMissingMetadataFailsClosed pins that an
// env-reference registration with no allowlist and no placeholder surface the
// ambiguity directly: the allowlist is empty and the placeholder is nil, so the
// recovery path can refuse rather than assume compatibility.
func TestParsePersistedEnvRefSecretsMissingMetadataFailsClosed(t *testing.T) {
	config := persistedConfigWithSecrets(
		`{"name":"JC_API_KEY","source":{"kind":"env","var":"JUST_CODE_HOST_PROJECT_JC_API_KEY"}}`,
	)
	refs, err := parsePersistedEnvRefSecrets(config)
	if err != nil {
		t.Fatalf("parsePersistedEnvRefSecrets: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("persisted secret refs = %v, want one project-secret registration", refs)
	}
	ref := refs[0]
	if len(ref.AllowedHosts) != 0 {
		t.Fatalf("AllowedHosts = %v, want empty when the document omits it", ref.AllowedHosts)
	}
	if ref.Placeholder != nil {
		t.Fatalf("Placeholder = %v, want nil when the document omits it", ref.Placeholder)
	}
}

// TestParsePersistedEnvRefSecretsReadsTheRuntimeDocumentShape pins the parser
// against a document copied from the runtime's own saved configuration, not
// from a shape I assumed. Three details here are easy to get wrong and each one
// silently breaks recovery rather than failing loudly:
//
//   - the secret policy is nested under network.secrets.secrets;
//   - allowlist entries are tagged host patterns ({"exact": ...} /
//     {"wildcard": ...}), so reading them as plain strings would fail the whole
//     decode and turn every credential-backed sandbox into a refusal;
//   - a wildcard entry must be reported as written, so validation can tell it
//     apart from an exact host it could actually verify against the approval.
func TestParsePersistedEnvRefSecretsReadsTheRuntimeDocumentShape(t *testing.T) {
	config := `{"name":"jc-proj","network":{"secrets":{"secrets":[` +
		`{"env_var":"API_KEY","source":{"kind":"env","var":"JUST_CODE_HOST_PROJECT_JC_API_KEY"},` +
		`"placeholder":"$MSB_API_KEY","allowed_hosts":[{"exact":"api.example.com"},{"wildcard":"*.openai.com"}]}]}}}`
	refs, err := parsePersistedEnvRefSecrets(config)
	if err != nil {
		t.Fatalf("parsePersistedEnvRefSecrets: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("persisted secret refs = %v, want one env-reference registration", refs)
	}
	ref := refs[0]
	if ref.HostEnv != "JUST_CODE_HOST_PROJECT_JC_API_KEY" {
		t.Fatalf("HostEnv = %q, want the persisted source variable", ref.HostEnv)
	}
	if len(ref.AllowedHosts) != 2 || ref.AllowedHosts[0] != "api.example.com" || ref.AllowedHosts[1] != "*.openai.com" {
		t.Fatalf("AllowedHosts = %v, want [api.example.com *.openai.com] from the tagged host patterns", ref.AllowedHosts)
	}
	if ref.Placeholder == nil || *ref.Placeholder != "$MSB_API_KEY" {
		t.Fatalf("Placeholder = %v, want %q", ref.Placeholder, "$MSB_API_KEY")
	}
}

// TestParsePersistedEnvRefSecretsReadsTheAnyHostVariant pins the third spelling
// of an allowlist entry. The runtime's catch-all variant serializes as the bare
// string "any" while the others are objects, so a parser that only accepted
// objects would fail to read any sandbox whose allowlist contains it. It is
// reported as the wildcard sentinel rather than dropped, so validation refuses
// it instead of mistaking an unrestricted allowlist for a narrow one.
func TestParsePersistedEnvRefSecretsReadsTheAnyHostVariant(t *testing.T) {
	config := `{"network":{"secrets":{"secrets":[` +
		`{"env_var":"API_KEY","source":{"kind":"env","var":"JUST_CODE_HOST_PROJECT_JC_API_KEY"},` +
		`"placeholder":"$MSB_API_KEY","allowed_hosts":["any"]}]}}}`
	refs, err := parsePersistedEnvRefSecrets(config)
	if err != nil {
		t.Fatalf("parsePersistedEnvRefSecrets: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("persisted secret refs = %v, want one env-reference registration", refs)
	}
	if len(refs[0].AllowedHosts) != 1 || refs[0].AllowedHosts[0] != hostAny {
		t.Fatalf("AllowedHosts = %v, want [%s] for the catch-all variant", refs[0].AllowedHosts, hostAny)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}

// TestWarnUnprotectedRuntimeNamesTheRuntimeAndTheRisk pins the user-facing
// warning for runtimes with no secret proxy: it must name the runtime, say the
// key is readable in the guest, and make clear that isolation backend is not
// an escape hatch.
func TestWarnUnprotectedRuntimeNamesTheRuntimeAndTheRisk(t *testing.T) {
	for _, runtimeName := range []string{"Tart", "agent-vm"} {
		out := captureStderr(t, func() { warnUnprotectedRuntime(runtimeName) })
		for _, want := range []string{runtimeName, "no secret injection", "inference credentials", "isolation backend and full"} {
			if !strings.Contains(out, want) {
				t.Fatalf("warning for %s missing %q: %s", runtimeName, want, out)
			}
		}
	}
}

// TestMicrosandboxStartRejectsIsolationSwitch pins the mode-switch guard: the
// start script is persisted at creation and cannot be rewritten, so switching
// isolation on an existing sandbox must fail with guidance instead of booting
// the wrong process.
func TestMicrosandboxLegacyOpenCodeScriptRequiresExplicitRecreation(t *testing.T) {
	for _, isolation := range []Isolation{IsolationFull, IsolationBackend} {
		for _, status := range []string{"stopped", "running"} {
			script := "exec opencode serve --hostname 0.0.0.0 --port 4096"
			if isolation == IsolationFull {
				script = "exec sleep infinity"
			}
			client := &fakeMSBClient{exists: true, status: status, startScript: script}
			m := newTestMicrosandbox(t, client)
			m.cfg.Isolation = isolation
			err := m.Start(context.Background())
			if err == nil || !strings.Contains(err.Error(), "legacy OpenCode") || !strings.Contains(err.Error(), "Export") || !strings.Contains(err.Error(), "recreate") {
				t.Fatalf("legacy immutable script needs an actionable, non-destructive refusal: %v", err)
			}
			if hasCall(client, "start "+msbSandbox) || hasCall(client, "stop "+msbSandbox) || hasCall(client, "remove "+msbSandbox) {
				t.Fatalf("legacy refusal must not change the VM: %v", client.calls)
			}
		}
	}
}

func TestMicrosandboxStartRejectsIsolationSwitch(t *testing.T) {
	// A sandbox created in full mode, now requested in backend mode.
	client := &fakeMSBClient{exists: true, status: "stopped", startScript: msbStartScript(IsolationFull)}
	m := newTestMicrosandbox(t, client)
	err := m.Start(context.Background())
	if err == nil {
		t.Fatal("switching an existing full-mode sandbox to backend must fail")
	}
	if !strings.Contains(err.Error(), "recreate") {
		t.Errorf("error must point at the recovery command: %v", err)
	}
	if hasCall(client, "start "+msbSandbox) || hasCall(client, "exec "+msbSandbox) {
		t.Fatalf("a mismatched sandbox must not be booted: %v", client.calls)
	}

	// The reverse direction: a backend-mode sandbox requested in full mode.
	client = &fakeMSBClient{exists: true, status: "stopped", startScript: msbStartScript(IsolationBackend)}
	m = newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("switching an existing backend-mode sandbox to full must fail")
	}
	if hasCall(client, "start "+msbSandbox) {
		t.Fatalf("a mismatched sandbox must not be booted: %v", client.calls)
	}
}

// TestMicrosandboxRestartLeavesMismatchedSandboxRunning pins the P07
// preflight: restart cannot fix a creation-fixed isolation mismatch, so it
// must refuse BEFORE stopping — stopping first would leave a previously
// usable sandbox down with an error that loops.
func TestMicrosandboxRestartLeavesMismatchedSandboxRunning(t *testing.T) {
	client := &fakeMSBClient{exists: true, status: "running", startScript: msbStartScript(IsolationFull)}
	m := newTestMicrosandbox(t, client)
	err := m.Restart(context.Background())
	if err == nil {
		t.Fatal("restart must refuse an isolation switch")
	}
	if !strings.Contains(err.Error(), "recreate") {
		t.Errorf("error must point at the recovery command: %v", err)
	}
	if hasCall(client, "stop "+msbSandbox) {
		t.Fatalf("a mismatched sandbox must not be stopped by restart: %v", client.calls)
	}
}

func TestMicrosandboxRestartLeavesGuestRunningWhenSkillPreflightFails(t *testing.T) {
	client := &fakeMSBClient{
		exists: true, status: "running", startScript: msbStartScript(IsolationBackend),
		execCaptureResults: []fakeMSBExecCaptureResult{{
			stderr: "managed skill directory exists without its project-skill inventory", code: 1,
		}},
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationBackend
	m.cfg.ProjectSkillsManaged = true
	err := m.Restart(context.Background())
	if err == nil || !strings.Contains(err.Error(), "project-skill inventory") {
		t.Fatalf("Restart error = %v, want a managed skill preflight refusal", err)
	}
	if hasCall(client, "stop "+msbSandbox) {
		t.Fatalf("Restart stopped the working guest before skill preflight: %v", client.calls)
	}
	if len(client.written) != 0 {
		t.Fatalf("read-only restart preflight wrote guest data: %+v", client.written)
	}
}

func TestReconcileVMRestartLeavesGuestRunningWhenSkillPreflightFails(t *testing.T) {
	client := &fakeMSBClient{
		exists: true, status: "running",
		execCaptureResults: []fakeMSBExecCaptureResult{{
			stderr: "managed skill directory exists without its project-skill inventory", code: 1,
		}},
	}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationBackend
	m.cfg.ProjectSkillsManaged = true
	err := m.applyReconcileOp(context.Background(), OpRestartVM, nil)
	if err == nil || !strings.Contains(err.Error(), "project-skill inventory") {
		t.Fatalf("reconcile restart error = %v, want a managed skill preflight refusal", err)
	}
	if hasCall(client, "stop "+msbSandbox) || hasCall(client, "start "+msbSandbox) {
		t.Fatalf("reconcile stopped the running guest before skill preflight: %v", client.calls)
	}
	if len(client.written) != 0 {
		t.Fatalf("read-only reconcile preflight wrote guest data: %+v", client.written)
	}
}

// TestMicrosandboxFullModeRunningSandboxIsReady records that a running
// full-mode sandbox is not health-probed (no backend endpoint exists in that
// mode, so probing would always fail and relaunch into the wrong process) —
// but readiness is still re-checked via the toolchain marker, because a
// previous creation may have timed out with the installer still running.
func TestMicrosandboxFullModeRunningSandboxIsReady(t *testing.T) {
	client := &fakeMSBClient{exists: true, status: "running", startScript: msbStartScript(IsolationFull)}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if hasCall(client, "exec "+msbSandbox+" "+msbRelaunchCommand) {
		t.Fatalf("a running full-mode sandbox must not be relaunched: %v", client.calls)
	}
	if !hasCall(client, "exec "+msbSandbox+" "+msbGuestPrepareProbe) {
		t.Fatalf("readiness marker was not re-checked: %v", client.calls)
	}
	if hasCall(client, "create") || hasCall(client, "start "+msbSandbox) {
		t.Fatalf("a running full-mode sandbox must be left as-is: %v", client.calls)
	}
}

// TestMicrosandboxStartMatchingIsolationProceeds records that the guard is
// mode-equality, not "always refuse": an existing sandbox whose script matches
// the requested mode still starts normally.
func TestMicrosandboxStartMatchingIsolationProceeds(t *testing.T) {
	client := &fakeMSBClient{exists: true, status: "stopped", startScript: msbStartScript(IsolationFull)}
	m := newTestMicrosandbox(t, client)
	m.cfg.Isolation = IsolationFull
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !hasCall(client, "start "+msbSandbox) {
		t.Fatalf("a matching sandbox must start; calls: %v", client.calls)
	}

	// An unreadable script must not block the start either.
	client = &fakeMSBClient{exists: true, status: "stopped"}
	m = newTestMicrosandbox(t, client)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start with an unknown script: %v", err)
	}
	if !hasCall(client, "start "+msbSandbox) {
		t.Fatalf("an undetectable mode must not block the start; calls: %v", client.calls)
	}
}

func TestMicrosandboxRunAgentAttachesTUI(t *testing.T) {
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{
		{stdout: `[{"id":"ses_existing"}]`, code: 0},
		{stdout: "--continue continue the last session", code: 0},
	}}
	m := newTestMicrosandbox(t, client)
	if err := m.RunAgent(context.Background()); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !hasCall(client, "attach "+msbSandbox+" opencode --standalone --continue /workspace") {
		t.Fatalf("RunAgent must resume the most recent session while attaching opencode at /workspace; calls: %v", client.calls)
	}
}

func TestMicrosandboxRunAgentFallsBackForOpenCodeWithoutContinue(t *testing.T) {
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{
		{stdout: `[{"id":"ses_existing"}]`, code: 0},
		{stdout: "OpenCode options: --model", code: 0},
	}}
	m := newTestMicrosandbox(t, client)
	if err := m.RunAgent(context.Background()); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !hasCall(client, "attach "+msbSandbox+" opencode --standalone /workspace") {
		t.Fatalf("an OpenCode CLI without --continue must still launch a fresh TUI session: %v", client.calls)
	}
}

func TestMicrosandboxRunAgentStartsNormallyWhenNoSessionExists(t *testing.T) {
	client := &fakeMSBClient{execCaptureResults: []fakeMSBExecCaptureResult{
		{stdout: "", code: 0},
	}}
	m := newTestMicrosandbox(t, client)
	if err := m.RunAgent(context.Background()); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !hasCall(client, "attach "+msbSandbox+" opencode --standalone /workspace") {
		t.Fatalf("first launch without a stored session must use plain OpenCode TUI: %v", client.calls)
	}
	if hasCall(client, "execcapture "+msbSandbox+" opencode --help") {
		t.Fatalf("a first launch must not probe or pass --continue without an existing session: %v", client.calls)
	}
}

func TestMicrosandboxRunAgentSurfacesExitCode(t *testing.T) {
	client := &fakeMSBClient{attachErr: errors.New("attach stream closed")}
	m := newTestMicrosandbox(t, client)
	if err := m.RunAgent(context.Background()); err == nil {
		t.Fatal("RunAgent must surface attach failures")
	}
}

func TestMicrosandboxStatus(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{exists: true, status: "running"})
	state, err := m.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(state, "running") {
		t.Fatalf("Status = %q", state)
	}
}

// TestMSBRestartRefusesRecordedSizingChange pins that restart does not keep a
// stale allocation silently: isolation already refuses on this path, and the
// sizing must not be the one creation-fixed attribute that is quietly kept.
func TestMSBRestartRefusesRecordedSizingChange(t *testing.T) {
	isolateHostState(t)
	client := &fakeMSBClient{exists: true, status: "running"}
	m := newTestMicrosandbox(t, client)
	m.cfg.CPUs, m.cfg.MemoryMB = 8, 16384

	path := instanceStatePath(DefaultStateDir(), m.InstanceName())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	applied := DesiredState{Instance: m.InstanceName(), Isolation: m.cfg.Isolation, Image: msbImage, CPUs: 4, MemoryMB: 8192}.toState()
	if err := WriteInstanceState(DefaultFS, path, applied); err != nil {
		t.Fatal(err)
	}
	err := m.rejectResourceSizingChange()
	if err == nil {
		t.Fatal("a recorded sizing change must refuse the restart")
	}
	for _, want := range []string{"8 CPUs", "16384", "recreate --microsandbox"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must mention %q: %v", want, err)
		}
	}
	// An unrecorded sizing is not a mismatch: it cannot be compared.
	legacy := DesiredState{Instance: m.InstanceName(), Isolation: m.cfg.Isolation, Image: msbImage}.toState()
	if err := WriteInstanceState(DefaultFS, path, legacy); err != nil {
		t.Fatal(err)
	}
	if err := m.rejectResourceSizingChange(); err != nil {
		t.Fatalf("an instance whose sizing was never recorded must restart: %v", err)
	}
	_ = os.RemoveAll(filepath.Dir(path))
}

// TestMSBInvalidSizingIsReported pins that an invalid JUST_CODE_CPUS reaches
// the user instead of behaving exactly like an unset variable.
func TestMSBInvalidSizingIsReported(t *testing.T) {
	m := newTestMicrosandbox(t, &fakeMSBClient{})
	m.cfg.SandboxResourcesErr = fmt.Errorf("JUST_CODE_CPUS must be at most 255, got 256")
	if err := m.validateConfig(); err == nil {
		t.Fatal("an invalid guest sizing must be reported before any runtime work")
	}
}

// Recover must boot a stopped, guest-owned sandbox with its persisted start
// script and wait only for a usable guest shell - never applying the current
// configuration (ModifyNextStart) and never relaunching the OpenCode backend.
func TestRecoverBootsStoppedSealedSandbox(t *testing.T) {
	client := sealedFakeMSB()
	m := newTestMicrosandbox(t, client)

	if err := m.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	joined := strings.Join(client.calls, "\n")
	// Sealed-workspace provenance is verified, and the instance is booted.
	if !strings.Contains(joined, "owned "+m.InstanceName()) {
		t.Fatalf("Recover must verify sealed workspace provenance, got calls:\n%s", joined)
	}
	if !strings.Contains(joined, "start "+m.InstanceName()) {
		t.Fatalf("Recover must boot the instance, got calls:\n%s", joined)
	}
	// It waits for a guest shell, not the backend: only the trivial probe runs.
	if !strings.Contains(joined, "exec "+m.InstanceName()+" echo ok") {
		t.Fatalf("Recover must probe for a guest shell, got calls:\n%s", joined)
	}
	if strings.Contains(joined, "modify "+m.InstanceName()) {
		t.Fatalf("Recover must not apply the current configuration via ModifyNextStart, got calls:\n%s", joined)
	}
	if strings.Contains(joined, msbRelaunchCommand) {
		t.Fatalf("Recover must never relaunch the OpenCode backend, got calls:\n%s", joined)
	}
}

// Recover on a sandbox that is already running still verifies the guest shell
// is reachable before reporting success.
func TestRecoverRunningSealedSandboxWaitsForGuestShell(t *testing.T) {
	client := sealedFakeMSB()
	client.status = "running"
	m := newTestMicrosandbox(t, client)

	if err := m.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	joined := strings.Join(client.calls, "\n")
	// A running sandbox is not started again, but the guest shell is still
	// probed - the sandbox may be up while the guest is not yet reachable.
	if strings.Contains(joined, "start "+m.InstanceName()) {
		t.Fatalf("Recover must not restart an already-running sandbox, got calls:\n%s", joined)
	}
	if !strings.Contains(joined, "exec "+m.InstanceName()+" echo ok") {
		t.Fatalf("Recover must still probe a running sandbox's guest shell, got calls:\n%s", joined)
	}
}

// Recover keeps the sealed-workspace guard fail-closed: a sandbox whose
// /workspace is a host bind mount is refused, not silently adopted.
func TestRecoverRefusesBindMountedWorkspace(t *testing.T) {
	client := &fakeMSBClient{exists: true, mount: "/host/checkout"}
	m := newTestMicrosandbox(t, client)

	err := m.Recover(context.Background())
	if err == nil {
		t.Fatal("Recover must refuse a bind-mounted workspace")
	}
	if !strings.Contains(err.Error(), "mounted from the host") {
		t.Fatalf("Recover refusal should name the host mount, got: %v", err)
	}
	// The guard must refuse before any boot attempt.
	if strings.Contains(strings.Join(client.calls, "\n"), "start ") {
		t.Fatalf("Recover must not boot a non-sealed sandbox, got calls:\n%s", client.calls)
	}
}

// Recover keeps the legacy-plaintext-key guard fail-closed: a sandbox whose
// guest environment still stores the real credential is refused.
func TestRecoverRefusesLegacyRawKey(t *testing.T) {
	client := sealedFakeMSB()
	client.env = map[string]string{msbAPISecretEnv: "an-exposed-value"}
	m := newTestMicrosandbox(t, client)

	err := m.Recover(context.Background())
	if err == nil {
		t.Fatal("Recover must refuse a sandbox storing a plaintext credential")
	}
	if !strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("Recover refusal should mention the plaintext credential, got: %v", err)
	}
	if strings.Contains(strings.Join(client.calls, "\n"), "start ") {
		t.Fatalf("Recover must not boot a legacy plaintext-key sandbox, got calls:\n%s", client.calls)
	}
}

// Recover on a credential-backed stopped sandbox publishes the host transport
// variable for the persisted credential reference during boot, matching the
// Start contract. Without it the guest start script cannot resolve the
// env-reference secret and the sandbox boots without its credential. The
// variable is restored once recovery returns: the raw value never lingers in
// the host environment.
func TestRecoverBootsStoppedSandboxWithHostSecrets(t *testing.T) {
	client := sealedFakeMSB()
	m := newTestMicrosandbox(t, client)
	m.cfg.APIKey = "" // force the stored-credential path
	m.credentialRead = func(context.Context, CredentialKind, string) (string, string, error) {
		return "stored-albert-secret", "native", nil
	}

	if err := m.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if got := client.startHostEnv["JUST_CODE_HOST_ALBERT_API_KEY"]; got != "stored-albert-secret" {
		t.Fatalf("Recover boot must publish the persisted credential transport variable, got %q", got)
	}
	if got := os.Getenv("JUST_CODE_HOST_ALBERT_API_KEY"); got != "" {
		t.Fatalf("Recover must restore the host environment after boot, %q still set", got)
	}
}

// Recover resolves the bindings actually persisted on the stopped sandbox, not
// the bindings the current provider configuration selects. A sandbox created
// with Albert but a configuration now using Mistral must boot with Albert's
// transport variable published and must not require a Mistral credential.
func TestRecoverUsesPersistedBindingsWhenProviderDiffers(t *testing.T) {
	client := sealedFakeMSB()
	client.persistedSecretRefs = []persistedSecretRef{{HostEnv: "JUST_CODE_HOST_ALBERT_API_KEY"}}
	m := newTestMicrosandbox(t, client)
	// The current configuration selects Mistral only; the persisted sandbox
	// was created with Albert, so only Albert must be republished.
	m.cfg.APIKey = ""
	m.cfg.Providers = []InferenceProvider{{
		Kind:           "mistral",
		Name:           "Mistral AI",
		CredentialKind: CredentialMistral,
		APIKeyEnv:      MistralAPIKeyEnv,
		Host:           "api.mistral.ai",
	}}
	m.credentialRead = func(context.Context, CredentialKind, string) (string, string, error) {
		return "albert-persisted-secret", "native", nil
	}

	if err := m.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if got := client.startHostEnv["JUST_CODE_HOST_ALBERT_API_KEY"]; got != "albert-persisted-secret" {
		t.Fatalf("Recover must publish the persisted Albert transport variable even when Mistral is configured, got %q", got)
	}
	if got := client.startHostEnv["JUST_CODE_HOST_MISTRAL_API_KEY"]; got != "" {
		t.Fatalf("Recover must not publish a Mistral transport variable the persisted sandbox did not reference, got %q", got)
	}
	if got := os.Getenv("JUST_CODE_HOST_ALBERT_API_KEY"); got != "" {
		t.Fatalf("Recover must restore the host environment after boot, %q still set", got)
	}
}

// Recover resolves a persisted project-secret registration through the approved
// project-secret metadata, not the current provider configuration. A sandbox
// that persisted both a built-in Albert transport variable and a project-secret
// transport variable must boot with both published, even when the current
// configuration selects a different provider, and the host environment must be
// restored once recovery returns.
func TestRecoverPublishesPersistedProjectSecretBinding(t *testing.T) {
	client := sealedFakeMSB()
	m := newTestMicrosandbox(t, client)
	// The current configuration selects Mistral only; the persisted sandbox
	// was created with Albert plus an approved project secret, so both must be
	// republished from what the sandbox actually persisted.
	m.cfg.APIKey = ""
	m.cfg.Providers = []InferenceProvider{{
		Kind:           "mistral",
		Name:           "Mistral AI",
		CredentialKind: CredentialMistral,
		APIKeyEnv:      MistralAPIKeyEnv,
		Host:           "api.mistral.ai",
	}}
	m.credentialRead = func(context.Context, CredentialKind, string) (string, string, error) {
		return "albert-persisted-secret", "native", nil
	}
	// Approved project-secret registration backing the persisted transport
	// variable. The store value is what recovery must publish, independent of
	// the current provider or binding configuration.
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, m.cfg.WorkspaceDir, m.StateDir, store)
	m.ProjectSecrets = s
	importProjectSecrets(t, s, "development", ".env", map[string]string{"API_KEY": "project-persisted-secret"})
	approveForTest(t, m, "API_KEY", "JC_API_KEY")
	// The persisted registration carries the metadata fixed at creation: the
	// destination allowlist and marker the approval produced. Recovery proves
	// these still match the current approval before publishing the value.
	client.persistedSecretRefs = []persistedSecretRef{
		{HostEnv: "JUST_CODE_HOST_ALBERT_API_KEY"},
		{
			HostEnv:      "JUST_CODE_HOST_PROJECT_JC_API_KEY",
			AllowedHosts: []string{"api.example.com"},
			Placeholder:  strPtr(ProjectSecretPlaceholder(s.Namespace(), "development", ".env", "API_KEY")),
		},
	}

	if err := m.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if got := client.startHostEnv["JUST_CODE_HOST_ALBERT_API_KEY"]; got != "albert-persisted-secret" {
		t.Fatalf("Recover must publish the persisted Albert transport variable, got %q", got)
	}
	if got := client.startHostEnv["JUST_CODE_HOST_PROJECT_JC_API_KEY"]; got != "project-persisted-secret" {
		t.Fatalf("Recover must publish the persisted project-secret transport variable, got %q", got)
	}
	if got := client.startHostEnv["JUST_CODE_HOST_MISTRAL_API_KEY"]; got != "" {
		t.Fatalf("Recover must not publish a Mistral transport variable the persisted sandbox did not reference, got %q", got)
	}
	for _, env := range []string{"JUST_CODE_HOST_ALBERT_API_KEY", "JUST_CODE_HOST_PROJECT_JC_API_KEY"} {
		if got := os.Getenv(env); got != "" {
			t.Fatalf("Recover must restore the host environment after boot, %s still set", env)
		}
	}
}

// preparePersistedProjectSecretRecovery builds a sealed fake runtime holding an
// approved proxy-protected project secret plus an imported value, and returns
// the pieces a persisted-registration case needs to describe itself. The caller
// sets client.persistedSecretRefs to express the registration metadata under
// test.
func preparePersistedProjectSecretRecovery(t *testing.T) (*fakeMSBClient, *MicrosandboxRuntime, *ProjectSecretStore, *projectSecretFakeStore) {
	t.Helper()
	client := sealedFakeMSB()
	m := newTestMicrosandbox(t, client)
	store := newProjectSecretFakeStore()
	s := projectSecretFixture(t, m.cfg.WorkspaceDir, m.StateDir, store)
	m.ProjectSecrets = s
	importProjectSecrets(t, s, "development", ".env", map[string]string{"API_KEY": "project-persisted-secret"})
	approveForTest(t, m, "API_KEY", "JC_API_KEY")
	return client, m, s, store
}

// Recover republishes a persisted project-secret registration whose allowlist is
// a strict subset of the current approval. The sandbox keeps the destinations it
// persisted at creation, so a destination approved later is not reachable by the
// booted guest and the operation stays safe.
func TestRecoverAcceptsPersistedProjectSecretDestinationSubset(t *testing.T) {
	client, m, s, _ := preparePersistedProjectSecretRecovery(t)
	// The current approval also grants a destination the sandbox never persisted.
	if err := ApproveProjectSecretBinding(DefaultFS, m.StateDir, s.Namespace(), ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "API_KEY",
		GuestEnv: "JC_API_KEY", Destinations: []string{"api.example.com", "api2.example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	client.persistedSecretRefs = []persistedSecretRef{{
		HostEnv:      "JUST_CODE_HOST_PROJECT_JC_API_KEY",
		AllowedHosts: []string{"api.example.com"},
		Placeholder:  strPtr(ProjectSecretPlaceholder(s.Namespace(), "development", ".env", "API_KEY")),
	}}

	if err := m.Recover(context.Background()); err != nil {
		t.Fatalf("Recover must accept a persisted allowlist that is a subset of the approval: %v", err)
	}
	// The published value is observed by the boot: the transport variable is
	// set while Start runs and restored afterwards, so assert the boot itself
	// happened on a compatible registration.
	if !hasCall(client, "start ") {
		t.Fatalf("Recover must boot the sandbox on a compatible subset registration, got calls:\n%s", strings.Join(client.calls, "\n"))
	}
	if got := client.startHostEnv["JUST_CODE_HOST_PROJECT_JC_API_KEY"]; got == "" {
		t.Fatalf("Recover must publish the persisted project-secret transport variable during boot, got %q", got)
	}
	if got := os.Getenv("JUST_CODE_HOST_PROJECT_JC_API_KEY"); got != "" {
		t.Fatalf("Recover must restore the host environment after boot, JUST_CODE_HOST_PROJECT_JC_API_KEY still set")
	}
}

// Recover refuses to republish a persisted project-secret registration whose
// allowlist carries a destination the current approval no longer grants.
// Recovery boots the persisted start script without ModifyNextStart, so the
// proxy would keep the older, wider allowlist and hand the freshly published
// value to a destination the user has since removed. The refusal must happen
// before the secret is read, and must tell the user to recreate the sandbox.
func TestRecoverRefusesPersistedProjectSecretWithRemovedDestination(t *testing.T) {
	client, m, s, store := preparePersistedProjectSecretRecovery(t)
	client.persistedSecretRefs = []persistedSecretRef{{
		HostEnv:      "JUST_CODE_HOST_PROJECT_JC_API_KEY",
		AllowedHosts: []string{"api.example.com", "revoked.example.com"},
		Placeholder:  strPtr(ProjectSecretPlaceholder(s.Namespace(), "development", ".env", "API_KEY")),
	}}
	before := store.getCount()

	err := m.Recover(context.Background())
	if err == nil {
		t.Fatal("Recover must refuse a persisted destination the current approval no longer grants")
	}
	if !strings.Contains(err.Error(), "not all still approved") {
		t.Fatalf("refusal should name the destination mismatch, got: %v", err)
	}
	if !strings.Contains(err.Error(), "recreate the sandbox") {
		t.Fatalf("refusal should tell the user to recreate the sandbox, got: %v", err)
	}
	if hasCall(client, "start ") {
		t.Fatalf("Recover must not boot the sandbox on an incompatible registration, got calls:\n%s", strings.Join(client.calls, "\n"))
	}
	if got := store.getCount() - before; got != 0 {
		t.Fatalf("Recover must not read the secret before proving compatibility, got %d reads", got)
	}
}

// Recover refuses a persisted project-secret registration whose placeholder no
// longer matches the marker the current approval derives: the guest-facing
// reference would not resolve, so booting would hand the guest an unusable
// value.
func TestRecoverRefusesPersistedProjectSecretWithStalePlaceholder(t *testing.T) {
	client, m, _, store := preparePersistedProjectSecretRecovery(t)
	client.persistedSecretRefs = []persistedSecretRef{{
		HostEnv:      "JUST_CODE_HOST_PROJECT_JC_API_KEY",
		AllowedHosts: []string{"api.example.com"},
		Placeholder:  strPtr("__JUST_CODE_SECRET_stale_marker__"),
	}}
	before := store.getCount()

	err := m.Recover(context.Background())
	if err == nil {
		t.Fatal("Recover must refuse a persisted placeholder that no longer matches the approval marker")
	}
	if !strings.Contains(err.Error(), "placeholder no longer matches") {
		t.Fatalf("refusal should name the placeholder mismatch, got: %v", err)
	}
	if hasCall(client, "start ") {
		t.Fatalf("Recover must not boot the sandbox on a stale placeholder, got calls:\n%s", strings.Join(client.calls, "\n"))
	}
	if got := store.getCount() - before; got != 0 {
		t.Fatalf("Recover must not read the secret before proving compatibility, got %d reads", got)
	}
}

// Recover fails closed when the persisted registration lacks the metadata
// needed to prove it still matches the approval. Absent metadata is ambiguous,
// not permission: recovery cannot distinguish a safe republish from a widened
// allowlist, so it refuses and points at recreation.
func TestRecoverRefusesPersistedProjectSecretWithoutProvenance(t *testing.T) {
	marker := func(t *testing.T, s *ProjectSecretStore) *string {
		t.Helper()
		return strPtr(ProjectSecretPlaceholder(s.Namespace(), "development", ".env", "API_KEY"))
	}
	cases := []struct {
		name    string
		ref     func(t *testing.T, s *ProjectSecretStore) persistedSecretRef
		wantErr string
	}{
		{
			name: "no destination allowlist",
			ref: func(t *testing.T, s *ProjectSecretStore) persistedSecretRef {
				return persistedSecretRef{HostEnv: "JUST_CODE_HOST_PROJECT_JC_API_KEY", Placeholder: marker(t, s)}
			},
			wantErr: "carries no destination allowlist",
		},
		{
			name: "no placeholder",
			ref: func(*testing.T, *ProjectSecretStore) persistedSecretRef {
				return persistedSecretRef{
					HostEnv:      "JUST_CODE_HOST_PROJECT_JC_API_KEY",
					AllowedHosts: []string{"api.example.com"},
				}
			},
			wantErr: "carries no placeholder",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, m, s, store := preparePersistedProjectSecretRecovery(t)
			client.persistedSecretRefs = []persistedSecretRef{tc.ref(t, s)}
			before := store.getCount()

			err := m.Recover(context.Background())
			if err == nil {
				t.Fatalf("Recover must fail closed when the persisted registration cannot be proven compatible (%s)", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("refusal should say %q, got: %v", tc.wantErr, err)
			}
			if hasCall(client, "start ") {
				t.Fatalf("Recover must not boot the sandbox on ambiguous metadata, got calls:\n%s", strings.Join(client.calls, "\n"))
			}
			if got := store.getCount() - before; got != 0 {
				t.Fatalf("Recover must not read the secret on ambiguous metadata, got %d reads", got)
			}
		})
	}
}

// Recover refuses to republish a configurable platform's credential when the
// persisted registration was created for a different endpoint than the one now
// configured for that credential kind.
//
// Every OpenAI-compatible platform shares one credential kind and one guest
// variable, so re-pointing OPENAI_COMPATIBLE_API_KEY at platform B while a
// sandbox still carries platform A's persisted registration would publish B's
// key to A's host: recovery keeps the persisted allowlist because it never calls
// ModifyNextStart. The refusal must happen before the value is resolved.
func TestRecoverRefusesPersistedProviderWhoseEndpointChanged(t *testing.T) {
	client := sealedFakeMSB()
	m := newTestMicrosandbox(t, client)
	// The configuration now points the shared variable at platform B.
	m.cfg.Providers = []InferenceProvider{{
		Kind:           ProviderOpenAICompatible,
		Name:           "Platform B",
		BaseURL:        "https://b.example.net/v1",
		CredentialKind: CredentialOpenAICompatible,
		APIKeyEnv:      OpenAICompatibleAPIKeyEnv,
		Host:           "b.example.net",
	}}
	resolved := false
	m.credentialRead = func(context.Context, CredentialKind, string) (string, string, error) {
		resolved = true
		return "platform-b-secret", "native", nil
	}
	// The stopped sandbox persisted platform A's registration and allowlist.
	client.persistedSecretRefs = []persistedSecretRef{{
		HostEnv:      "JUST_CODE_HOST_OPENAI_COMPATIBLE_API_KEY",
		AllowedHosts: []string{"a.example.com"},
		Placeholder:  strPtr("$MSB_OPENAI_COMPATIBLE_API_KEY"),
	}}

	err := m.Recover(context.Background())
	if err == nil {
		t.Fatal("Recover must refuse when the configured platform no longer matches the persisted registration")
	}
	if !strings.Contains(err.Error(), "no longer include the configured platform's host") {
		t.Fatalf("refusal should name the endpoint mismatch, got: %v", err)
	}
	if resolved {
		t.Fatal("Recover must not resolve the credential before proving the endpoint still matches")
	}
	if hasCall(client, "start ") {
		t.Fatalf("Recover must not boot the sandbox on a re-pointed credential, got calls:\n%s", strings.Join(client.calls, "\n"))
	}
}

// Recover still accepts a persisted provider registration whose allowlist does
// contain the configured platform's host: that is the ordinary case, and the new
// endpoint check must not disturb it.
func TestRecoverAcceptsPersistedProviderWhoseEndpointStillMatches(t *testing.T) {
	client := sealedFakeMSB()
	m := newTestMicrosandbox(t, client)
	m.cfg.Providers = []InferenceProvider{{
		Kind:           ProviderOpenAICompatible,
		Name:           "Platform A",
		BaseURL:        "https://a.example.com/v1",
		CredentialKind: CredentialOpenAICompatible,
		APIKeyEnv:      OpenAICompatibleAPIKeyEnv,
		Host:           "a.example.com",
	}}
	m.credentialRead = func(context.Context, CredentialKind, string) (string, string, error) {
		return "platform-a-secret", "native", nil
	}
	client.persistedSecretRefs = []persistedSecretRef{{
		HostEnv:      "JUST_CODE_HOST_OPENAI_COMPATIBLE_API_KEY",
		AllowedHosts: []string{"a.example.com"},
		Placeholder:  strPtr("$MSB_OPENAI_COMPATIBLE_API_KEY"),
	}}

	if err := m.Recover(context.Background()); err != nil {
		t.Fatalf("Recover must accept a persisted registration that still matches the configured endpoint: %v", err)
	}
	if got := client.startHostEnv["JUST_CODE_HOST_OPENAI_COMPATIBLE_API_KEY"]; got != "platform-a-secret" {
		t.Fatalf("Recover must publish the persisted provider transport variable, got %q", got)
	}
}

// Recover refuses a sandbox that does not exist, telling the user to start it.
func TestRecoverRefusesMissingSandbox(t *testing.T) {
	client := &fakeMSBClient{}
	m := newTestMicrosandbox(t, client)

	err := m.Recover(context.Background())
	if err == nil {
		t.Fatal("Recover must refuse a nonexistent sandbox")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Recover refusal should say the sandbox does not exist, got: %v", err)
	}
}

// Recover fails with a bounded timeout when the guest shell never becomes
// reachable, instead of launching the backend or hanging.
func TestRecoverTimesOutWhenGuestShellUnreachable(t *testing.T) {
	client := sealedFakeMSB()
	client.execDefault = &fakeMSBExecResult{code: 127, stderr: "sh: not found"}
	m := newTestMicrosandbox(t, client)
	m.cfg.StartTimeout = 20 * time.Millisecond
	m.toolchainPollDelay = time.Millisecond

	err := m.Recover(context.Background())
	if err == nil {
		t.Fatal("Recover must fail when the guest shell never becomes reachable")
	}
	if !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("Recover failure should state the guest shell was unreachable, got: %v", err)
	}
	if strings.Contains(strings.Join(client.calls, "\n"), msbRelaunchCommand) {
		t.Fatalf("Recover must not relaunch the backend on a shell timeout, got calls:\n%s", client.calls)
	}
}
