package codebox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// gitInit runs git init in dir, creating a real worktree so discovery tests
// exercise the real `git rev-parse --show-toplevel` path.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", dir, err, out)
	}
}

func TestDiscoverProjectGitRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	gitInit(t, base)
	sub := filepath.Join(base, "sub", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	pc, err := DiscoverProject(sub)
	if err != nil {
		t.Fatalf("DiscoverProject: %v", err)
	}
	// DiscoverProject resolves symlinks, and on macOS t.TempDir() lives
	// under /var -> /private/var; compare against the resolved form.
	want, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	if pc.Root != want {
		t.Errorf("Root = %q, want worktree root %q", pc.Root, want)
	}
	if !pc.IsGit {
		t.Error("IsGit = false, want true")
	}
	if pc.Name != filepath.Base(base) {
		t.Errorf("Name = %q, want %q", pc.Name, filepath.Base(base))
	}
}

func TestDiscoverProjectNonGitUsesDirectory(t *testing.T) {
	base := t.TempDir()
	pc, err := DiscoverProject(base)
	if err != nil {
		t.Fatalf("DiscoverProject: %v", err)
	}
	want, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	if pc.Root != want {
		t.Errorf("Root = %q, want %q", pc.Root, want)
	}
	if pc.IsGit {
		t.Error("IsGit = true for a non-Git directory")
	}
}

func TestDiscoverProjectSymlinkAliasResolvesConsistently(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	gitInit(t, base)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(base, alias); err != nil {
		t.Skip("symlinks unavailable")
	}
	direct, err := DiscoverProject(base)
	if err != nil {
		t.Fatal(err)
	}
	viaAlias, err := DiscoverProject(alias)
	if err != nil {
		t.Fatal(err)
	}
	if direct.Root != viaAlias.Root {
		t.Errorf("symlink alias changed identity: %q vs %q", direct.Root, viaAlias.Root)
	}
	if direct.InstanceName() != viaAlias.InstanceName() {
		t.Errorf("symlink alias changed instance name: %q vs %q", direct.InstanceName(), viaAlias.InstanceName())
	}
}

func TestInstanceNameSameNamedReposDoNotCollide(t *testing.T) {
	a := InstanceName("/home/alice/Code/org/codebox", "codebox")
	b := InstanceName("/home/alice/Code/other/codebox", "codebox")
	if a == b {
		t.Fatalf("two same-named repositories got the same instance name: %q", a)
	}
	if !strings.HasPrefix(a, "cb-codebox-") {
		t.Errorf("name %q is not readable (want cb-codebox-<suffix>)", a)
	}
}

func TestInstanceNameWorktreesDoNotCollide(t *testing.T) {
	main := InstanceName("/home/u/repo", "repo")
	wt := InstanceName("/home/u/.worktrees/repo-feature", "repo-feature")
	if main == wt {
		t.Fatalf("worktree and main checkout got the same instance name: %q", main)
	}
}

func TestInstanceNameStableAcrossCalls(t *testing.T) {
	root := "/home/u/Code/org/proj"
	if InstanceName(root, "proj") != InstanceName(root, "proj") {
		t.Fatal("instance name is not deterministic")
	}
}

func TestInstanceNameSafeCharacters(t *testing.T) {
	got := InstanceName("/tmp/weird name with spaces", "weird name with spaces")
	for _, r := range got {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			t.Fatalf("instance name %q contains unsafe character %q", got, r)
		}
	}
}

func TestPathSuffixNormalizesWindowsSeparators(t *testing.T) {
	if runtime.GOOS != "windows" {
		// filepath.ToSlash is a no-op on non-Windows, so the normalization is
		// only observable there; the property tested here is that the suffix
		// is derived from the cleaned, ToSlash'd path.
		got := pathSuffix(filepath.Join("home", "u", "proj"))
		want := pathSuffix("/home/u/proj")
		if got != want {
			t.Errorf("pathSuffix(%q) = %q, want %q", filepath.Join("home", "u", "proj"), got, want)
		}
	}
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := NewProjectRegistry("/reg/registry.json")
	r.FS = newMapFS()
	if err := r.Register("/proj/a", "cb-a-11111"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	inst, ok, err := r.Lookup("/proj/a")
	if err != nil || !ok || inst != "cb-a-11111" {
		t.Fatalf("Lookup = %q, %v, %v", inst, ok, err)
	}
	// Idempotent re-registration.
	if err := r.Register("/proj/a", "cb-a-11111"); err != nil {
		t.Fatalf("idempotent Register: %v", err)
	}
}

func TestRegistryPersistsAcrossInstances(t *testing.T) {
	fs := newMapFS()
	r1 := NewProjectRegistry("/reg/registry.json")
	r1.FS = fs
	if err := r1.Register("/proj/a", "cb-a-11111"); err != nil {
		t.Fatal(err)
	}
	r2 := NewProjectRegistry("/reg/registry.json")
	r2.FS = fs
	inst, ok, err := r2.Lookup("/proj/a")
	if err != nil || !ok || inst != "cb-a-11111" {
		t.Fatalf("second registry instance Lookup = %q, %v, %v", inst, ok, err)
	}
}

func TestRegistryRefusesInstanceCollision(t *testing.T) {
	r := NewProjectRegistry("/reg/registry.json")
	r.FS = newMapFS()
	if err := r.Register("/proj/a", "cb-a-11111"); err != nil {
		t.Fatal(err)
	}
	err := r.Register("/proj/b", "cb-a-11111")
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err = %v, want duplicate-registration refusal", err)
	}
}

func TestRegistryRefusesSilentRebind(t *testing.T) {
	r := NewProjectRegistry("/reg/registry.json")
	r.FS = newMapFS()
	if err := r.Register("/proj/a", "cb-a-11111"); err != nil {
		t.Fatal(err)
	}
	err := r.Register("/proj/a", "cb-a-22222")
	if err == nil || !strings.Contains(err.Error(), "explicit rebind") {
		t.Fatalf("err = %v, want silent-rebind refusal", err)
	}
	// Explicit rebind is allowed.
	if err := r.Rebind("/proj/a", "cb-a-22222"); err != nil {
		t.Fatalf("Rebind: %v", err)
	}
	inst, _, _ := r.Lookup("/proj/a")
	if inst != "cb-a-22222" {
		t.Errorf("after rebind, instance = %q, want cb-a-22222", inst)
	}
}

func TestRegistryRejectsNewerSchema(t *testing.T) {
	fs := newMapFS()
	fs.files["/reg/registry.json"] = []byte(`{"schemaVersion": 99, "entries": []}`)
	r := NewProjectRegistry("/reg/registry.json")
	r.FS = fs
	_, _, err := r.Lookup("/proj/a")
	if err == nil || !strings.Contains(err.Error(), "newer than this build supports") {
		t.Fatalf("err = %v, want schema refusal", err)
	}
}

func TestProjectLockAcquireRelease(t *testing.T) {
	l := &ProjectLock{Path: "/locks/demo.lock", FS: newMapFS()}
	release, err := l.Acquire()
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	release()
	// Re-acquiring after release must succeed.
	release2, err := l.Acquire()
	if err != nil {
		t.Fatalf("re-Acquire: %v", err)
	}
	release2()
}

// TestProjectLockReleaseRetriesTransientRemoveFailure proves the release
// path survives a Remove that fails a few times before succeeding — the
// Windows shape where an antivirus/indexer handle briefly denies delete
// access. A single-attempt release leaks a lock whose PID still looks
// alive, which processAlive's conservative Windows behaviour cannot steal
// back, wedging the project.
func TestProjectLockReleaseRetriesTransientRemoveFailure(t *testing.T) {
	dir := t.TempDir()
	fs := &flakyRemoveFS{FS: DefaultFS, failFor: 3, path: filepath.Join(dir, "project-update.lock")}
	lock := &ProjectLock{Path: fs.path, FS: fs}
	release, err := lock.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(lock.Path); !os.IsNotExist(err) {
		t.Fatalf("lock file survived release: %v (remove attempts: %d)", err, fs.attempts)
	}
	if fs.attempts != fs.failFor+1 {
		t.Fatalf("release gave up after %d attempts; want %d (retry through transient failures)", fs.attempts, fs.failFor+1)
	}
}

// TestProjectLockReleaseGivesUpAfterBoundedAttempts pins the retry cap: a
// permanently failing Remove must not spin forever.
func TestProjectLockReleaseGivesUpAfterBoundedAttempts(t *testing.T) {
	dir := t.TempDir()
	fs := &flakyRemoveFS{FS: DefaultFS, failFor: 100, path: filepath.Join(dir, "project-update.lock")}
	lock := &ProjectLock{Path: fs.path, FS: fs, WaitTimeout: time.Second}
	release, err := lock.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	release()
	if fs.attempts > 8 {
		t.Fatalf("release retried %d times; want the 8-attempt cap", fs.attempts)
	}
	if _, err := os.Stat(fs.path); os.IsNotExist(err) {
		t.Fatal("lock file was removed despite permanent remove failures")
	}
}

// TestProjectLockAcquireRetriesTransientReadFailure proves the waiter
// survives a ReadFile that fails transiently on the lock path — the
// Windows shape where an antivirus/indexer handle briefly denies read
// access to the freshly written lockfile. Aborting on the first read
// error made a concurrent second invocation fail to acquire the lock.
func TestProjectLockAcquireRetriesTransientReadFailure(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "project-update.lock")
	// The holder uses the plain FS: the flaky wrapper is the waiter's
	// view of the lock, whose ReadFile hits the transient failures.
	holder := &ProjectLock{Path: lockPath, FS: DefaultFS}
	holderRelease, err := holder.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer holderRelease()
	fs := &flakyReadFS{FS: DefaultFS, path: lockPath, failFor: 3}
	lock := &ProjectLock{Path: lockPath, FS: fs, WaitTimeout: 5 * time.Second, PollInterval: time.Millisecond}
	go func() {
		time.Sleep(50 * time.Millisecond)
		holderRelease()
	}()
	if _, err := lock.Acquire(); err != nil {
		t.Fatalf("waiter failed to acquire through transient read failures: %v", err)
	}
	if fs.reads < fs.failFor {
		t.Fatalf("waiter did not retry through read failures (reads: %d)", fs.reads)
	}
}

// TestProjectLockAcquireReturnsPermanentReadError pins that a permanently
// unreadable lock path (e.g. a directory at the lock path) fails fast with
// the underlying read error, not a generic timeout after spinning for the
// full wait budget.
func TestProjectLockAcquireReturnsPermanentReadError(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "project-update.lock")
	if err := os.Mkdir(lockPath, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := &ProjectLock{Path: lockPath, FS: DefaultFS, WaitTimeout: 300 * time.Millisecond, PollInterval: time.Millisecond}
	started := time.Now()
	_, err := lock.Acquire()
	if err == nil {
		t.Fatal("Acquire succeeded with a directory at the lock path")
	}
	// The underlying error surfaces via different branches per OS (on
	// Windows the exclusive create on a directory fails directly), so
	// assert on what the code owns: the actionable error, not a generic
	// timeout.
	if strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v; want the underlying read error, not a generic timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Acquire spun for %s; want it to stop at the wait deadline", elapsed)
	}
}

// flakyReadFS fails ReadFile on path the first failFor times, mirroring a
// transient sharing violation. Other paths delegate untouched.
type flakyReadFS struct {
	FS
	failFor int
	path    string
	reads   int
}

func (f *flakyReadFS) ReadFile(path string) ([]byte, error) {
	if path != f.path {
		return f.FS.ReadFile(path)
	}
	f.reads++
	if f.reads <= f.failFor {
		return nil, fmt.Errorf("sharing violation (transient)")
	}
	return f.FS.ReadFile(path)
}

// flakyRemoveFS fails Remove on path the first failFor times, mirroring a
// transient sharing violation. Other paths delegate untouched.
type flakyRemoveFS struct {
	FS
	failFor  int
	path     string
	attempts int
}

func (f *flakyRemoveFS) Remove(path string) error {
	if path != f.path {
		return f.FS.Remove(path)
	}
	f.attempts++
	if f.attempts <= f.failFor {
		return fmt.Errorf("sharing violation (transient)")
	}
	return f.FS.Remove(path)
}

func TestProjectLockConcurrentSetupSingleWinner(t *testing.T) {
	// The mapFS WriteFile cannot fail, so the lock's exclusive property is
	// exercised against the real filesystem: two concurrent Acquires must
	// serialize, never both succeed at once.
	dir := t.TempDir()
	l1 := &ProjectLock{Path: filepath.Join(dir, "p.lock"), FS: DefaultFS}
	l2 := &ProjectLock{Path: filepath.Join(dir, "p.lock"), FS: DefaultFS}
	r1, err := l1.Acquire()
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	acquired2 := make(chan error, 1)
	go func() {
		// This must block until the first holder releases; it cannot just
		// succeed. The test bounds the wait so a broken lock fails fast.
		r2, err := l2.Acquire()
		if err == nil {
			r2()
		}
		acquired2 <- err
	}()
	select {
	case err := <-acquired2:
		t.Fatalf("second Acquire returned while lock held: %v", err)
	case <-time.After(200 * time.Millisecond):
		// Still blocked: correct.
	}
	r1()
	select {
	case err := <-acquired2:
		if err != nil {
			t.Fatalf("second Acquire after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire never returned after release")
	}
}

func TestMicrosandboxInstanceNameThreading(t *testing.T) {
	// A backend built for an instance must operate on that instance, not the
	// legacy singleton: every client call carries the project name.
	client := &fakeMSBClient{exists: true, status: "running"}
	m := NewMicrosandboxRuntimeForInstance(Config{
		APIKey:       "key",
		WorkspaceDir: t.TempDir(),
		Username:     "opencode",
		Password:     "pw",
	}, "cb-demo-ab12c")
	m.Client = client
	if m.InstanceName() != "cb-demo-ab12c" {
		t.Fatalf("InstanceName = %q", m.InstanceName())
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasCall(client, "stop cb-demo-ab12c") {
		t.Fatalf("Stop did not target the project instance; calls: %v", client.calls)
	}
}

func TestMicrosandboxDefaultStaysLegacySingleton(t *testing.T) {
	// Compatibility: a backend built without an instance keeps operating on
	// the legacy singleton name, so existing users' sandboxes are found.
	m := NewMicrosandboxRuntime(Config{APIKey: "key", WorkspaceDir: t.TempDir()})
	if m.InstanceName() != msbSandbox {
		t.Fatalf("default InstanceName = %q, want legacy %q", m.InstanceName(), msbSandbox)
	}
}

func TestMicrosandboxLegacyInstanceNotAdopted(t *testing.T) {
	// The registry is the authority: a legacy-named sandbox that was never
	// registered for this project must not be adopted silently. The lookup
	// returns no registration, so the caller must fail closed rather than
	// operate on a name that happens to exist.
	r := NewProjectRegistry("/reg/registry.json")
	r.FS = newMapFS()
	_, ok, err := r.Lookup("/proj/demo")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("unregistered project resolved to a registration")
	}
}

// TestRegistryCrossProcessRegistrationIsNotLost pins the P05 review fix:
// two registry objects (two CLI processes) registering different projects
// concurrently must both survive. Without the cross-process file lock, the
// last rename silently discarded the other registration.
func TestRegistryCrossProcessRegistrationIsNotLost(t *testing.T) {
	fs := newMapFS()
	var wg sync.WaitGroup
	roots := []string{"/proj/a", "/proj/b", "/proj/c", "/proj/d"}
	for i, root := range roots {
		wg.Add(1)
		go func(i int, root string) {
			defer wg.Done()
			r := NewProjectRegistry("/reg/registry.json")
			r.FS = fs
			if err := r.Register(root, fmt.Sprintf("cb-%d", i)); err != nil {
				t.Errorf("Register(%s): %v", root, err)
			}
		}(i, root)
	}
	wg.Wait()
	// A fresh reader must see all four registrations.
	r := NewProjectRegistry("/reg/registry.json")
	r.FS = fs
	for i, root := range roots {
		inst, ok, err := r.Lookup(root)
		if err != nil || !ok || inst != fmt.Sprintf("cb-%d", i) {
			t.Errorf("Lookup(%s) = %q, %v, %v; registration was lost", root, inst, ok, err)
		}
	}
}

// TestRegistryRollsBackWhenSaveFails pins the second P05 review fix: if the
// write fails, the in-memory entry is rolled back so a retry on the same
// object cannot report success from the idempotent branch while nothing
// was persisted.
type failingWriteFS struct {
	FS
	fail bool
}

func (f *failingWriteFS) WriteTemp(dir, base string, data []byte, perm os.FileMode) (string, error) {
	if f.fail {
		return "", fmt.Errorf("simulated write failure")
	}
	return f.FS.WriteTemp(dir, base, data, perm)
}

func TestRegistryRollsBackWhenSaveFails(t *testing.T) {
	fs := &failingWriteFS{FS: newMapFS(), fail: true}
	r := NewProjectRegistry("/reg/registry.json")
	r.FS = fs
	if err := r.Register("/proj/a", "cb-a"); err == nil {
		t.Fatal("Register must surface the write failure")
	}
	// The entry must not linger in memory: the idempotent branch would
	// otherwise return success on retry while the file was never written.
	if _, ok, _ := r.Lookup("/proj/a"); ok {
		t.Fatal("failed Register left the entry in memory")
	}
	// Retry with the failure cleared: it must now succeed and persist.
	fs.fail = false
	if err := r.Register("/proj/a", "cb-a"); err != nil {
		t.Fatalf("retry after transient failure: %v", err)
	}
	r2 := NewProjectRegistry("/reg/registry.json")
	r2.FS = fs
	if inst, ok, err := r2.Lookup("/proj/a"); err != nil || !ok || inst != "cb-a" {
		t.Fatalf("persisted lookup = %q, %v, %v", inst, ok, err)
	}
}

// TestProjectLockStaleStealDoesNotDeleteLiveLock pins the third P05 review
// fix: when two waiters observe the same stale lock, the loser of the steal
// race must not delete the winner's freshly created live lock.
func TestProjectLockStaleStealDoesNotDeleteLiveLock(t *testing.T) {
	fs := newMapFS()
	lockPath := "/reg/proj/setup.lock"
	if err := fs.MkdirAll("/reg/proj", 0o755); err != nil {
		t.Fatal(err)
	}
	// A provably stale lock: the PID cannot exist on this host.
	if err := fs.WriteFile(lockPath, []byte("999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	acquired := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := &ProjectLock{Path: lockPath, FS: fs}
			release, err := l.Acquire()
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			acquired <- struct{}{}
			// Hold briefly so the other waiter exercises the contended path.
			time.Sleep(30 * time.Millisecond)
			release()
		}()
	}
	wg.Wait()
	// Both waiters must have acquired (serially); neither errored, and the
	// lockfile is gone at the end.
	if len(acquired) != 2 {
		t.Fatalf("both waiters must acquire the lock, got %d", len(acquired))
	}
	if _, err := fs.ReadFile(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lockfile must be released, err = %v", err)
	}
}

func TestProjectLockReapsAbandonedStealMarker(t *testing.T) {
	fs := newMapFS()
	lockPath := "/reg/proj/update.lock"
	if err := fs.MkdirAll("/reg/proj", 0o755); err != nil {
		t.Fatal(err)
	}
	stale := []byte("2147483647 1 1\n")
	if err := fs.CreateExclusive(lockPath, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fs.CreateExclusive(lockPath+".steal", stale, 0o600); err != nil {
		t.Fatal(err)
	}
	lock := &ProjectLock{Path: lockPath, FS: fs, WaitTimeout: time.Second, PollInterval: time.Millisecond}
	release, err := lock.Acquire()
	if err != nil {
		t.Fatalf("Acquire with abandoned steal marker: %v", err)
	}
	release()
	for _, path := range []string{lockPath, lockPath + ".steal"} {
		if _, err := fs.ReadFile(path); !os.IsNotExist(err) {
			t.Fatalf("stale lock path %s remains, err = %v", path, err)
		}
	}
}

func TestProjectLockTimesOutWithPathAndHolderPID(t *testing.T) {
	fs := newMapFS()
	lockPath := "/reg/proj/live.lock"
	if err := fs.MkdirAll("/reg/proj", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteFile(lockPath, []byte(fmt.Sprintf("%d 1 1\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	lock := &ProjectLock{Path: lockPath, FS: fs, WaitTimeout: 60 * time.Millisecond, PollInterval: 5 * time.Millisecond}
	if _, err := lock.Acquire(); err == nil || !strings.Contains(err.Error(), lockPath) || !strings.Contains(err.Error(), fmt.Sprint(os.Getpid())) {
		t.Fatalf("Acquire error = %v; want bounded error naming path and holder PID", err)
	}
}

func TestProjectLockTimeoutCoversRepeatedOwnershipChanges(t *testing.T) {
	lockPath := "/reg/proj/churning.lock"
	fs := &churningProjectLockFS{FS: newMapFS(), Path: lockPath}
	lock := &ProjectLock{Path: lockPath, FS: fs, WaitTimeout: 35 * time.Millisecond, PollInterval: time.Millisecond}
	started := time.Now()
	if _, err := lock.Acquire(); err == nil || !strings.Contains(err.Error(), lockPath) {
		t.Fatalf("Acquire error = %v; want bounded timeout naming lock path", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("Acquire exceeded bounded wait under lock-file churn: %s", elapsed)
	}
}

type churningProjectLockFS struct {
	FS
	Path  string
	Reads int
}

func (f *churningProjectLockFS) CreateExclusive(path string, _ []byte, _ os.FileMode) error {
	if path == f.Path {
		return os.ErrExist
	}
	return f.FS.CreateExclusive(path, nil, 0o600)
}

func (f *churningProjectLockFS) ReadFile(path string) ([]byte, error) {
	if path == f.Path {
		f.Reads++
		return []byte(fmt.Sprintf("changing-%d", f.Reads)), nil
	}
	return f.FS.ReadFile(path)
}
