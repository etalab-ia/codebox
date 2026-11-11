package codebox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Project discovery and instance identity (P05). The goal is to stop reusing
// one sandbox for unrelated checkouts: every project gets a deterministic,
// readable instance name derived from its canonical root, and a host-local
// registry records which root owns which instance.

// ProjectContext is the resolved identity of the project being operated on.
type ProjectContext struct {
	// Root is the canonical project root: the Git worktree root when the
	// directory is inside a worktree, otherwise the explicit override or the
	// current directory. It is absolute and symlink-resolved so aliases
	// resolve consistently.
	Root string
	// Name is the readable project name (the root's base name).
	Name string
	// IsGit records whether the root is a Git worktree. The sealed workspace
	// (P22) has no clone source for a non-Git root; discovery records the
	// class so later stages know which transfer path applies. P22's decision
	// (docs/decisions/2026-09-24-sealed-workspace.md) is a filtered snapshot
	// for a non-Git root, not a rejection, so this flag selects the source
	// set (git's view of the working tree, or a directory walk) rather than
	// gating the project.
	IsGit bool
	// Explicit records that the root came from an explicit override rather
	// than discovery.
	Explicit bool
}

// InstanceName is the deterministic sandbox/VM instance name for a project:
// "cb-" + readable name + "-" + short path-derived suffix. The suffix keeps
// two same-named repositories and two worktrees from colliding; the readable
// prefix keeps `msb list` output and error messages human.
func (p ProjectContext) InstanceName() string {
	return InstanceName(p.Root, p.Name)
}

// InstanceName derives the instance name from a root and readable name.
func InstanceName(root, name string) string {
	suffix := pathSuffix(root)
	if suffix == "" {
		return "cb-" + safeFileName(name)
	}
	return "cb-" + safeFileName(name) + "-" + suffix
}

// pathSuffix derives a short, stable, filesystem-safe suffix from a path.
// Two different roots must collide only with probability ~2^-20 (five hex
// characters); the same root always yields the same suffix, including across
// machines when the path differs only by a leading mount point (the hash is
// over path segments, not the raw string, so "/home/u/p" and "/Users/u/p"
// hash differently but "C:\\p" and "C:/p" normalize identically).
func pathSuffix(root string) string {
	normalized := filepath.ToSlash(filepath.Clean(root))
	// Drop a Windows drive prefix: it is machine-local layout, not project
	// identity, and two clones of the same repo on C: and D: are still the
	// same project for collision-avoidance purposes.
	normalized = strings.TrimPrefix(normalized, "/")
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:])[:5]
}

// DiscoverProject resolves the project context for a directory. The Git
// worktree root is used when the directory is inside a worktree; otherwise
// the directory itself. The returned root is absolute and symlink-resolved,
// so symlink aliases of the same checkout resolve to one project identity.
// Explicit root overrides are a P06 concern; discovery here never scans
// above the worktree root or the given directory.
func DiscoverProject(dir string) (ProjectContext, error) {
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ProjectContext{}, err
		}
		dir = cwd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ProjectContext{}, err
	}
	// Resolve symlinks first, so a link into a worktree discovers the real
	// worktree root rather than a link path outside it.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return ProjectContext{}, fmt.Errorf("resolve project directory %s: %w", abs, err)
	}
	root := resolved
	if top, err := gitWorktreeRoot(resolved); err == nil {
		root = top
	}
	return ProjectContext{
		Root:     root,
		Name:     filepath.Base(root),
		IsGit:    gitWorktreeRootExists(root),
		Explicit: false,
	}, nil
}

// gitWorktreeRoot returns the absolute worktree root of dir when it is inside
// a Git worktree. It shells out to git so worktrees (whose .git is a file
// pointing at the shared main worktree) resolve correctly without reimplement
// -ing Git's discovery rules. An error means "not inside a worktree" or "git
// unavailable"; callers treat both as non-Git.
func gitWorktreeRoot(dir string) (string, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return "", err
	}
	cmd := exec.Command(git, "-C", dir, "rev-parse", "--show-toplevel")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git worktree: %w", err)
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", fmt.Errorf("git rev-parse --show-toplevel returned nothing for %s", dir)
	}
	return filepath.FromSlash(top), nil
}

// gitWorktreeRootExists reports whether root is itself a Git worktree root
// (has a .git entry). It avoids spawning git: the discovery above already
// established worktree membership; this is the cheap classification for the
// recorded context.
func gitWorktreeRootExists(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".git"))
	return err == nil
}

// ProjectRegistry records project -> instance ownership on the host, outside
// every workspace. It is the authority for "which instance belongs to this
// root" so a moved or renamed checkout can be detected instead of silently
// adopting an unrelated VM.
//
// Mutations serialize across processes through an exclusive file lock
// (registry.lock next to the registry file): two CLI processes registering
// different projects concurrently must not lose one registration to a
// last-rename-wins race. The in-memory mutex alone cannot provide that —
// it only protects one Go object.
type ProjectRegistry struct {
	// Path is the registry file path (host state).
	Path string
	// FS abstracts file access for tests; production uses DefaultFS.
	FS FS

	mu sync.Mutex
	// entries is the in-memory registry: root -> instance name.
	entries map[string]string
	loaded  bool
}

// NewProjectRegistry builds a registry backed by path.
func NewProjectRegistry(path string) *ProjectRegistry {
	return &ProjectRegistry{Path: path, FS: DefaultFS, entries: map[string]string{}}
}

// lockPath is the cross-process mutation lock for the registry.
func (r *ProjectRegistry) lockPath() string {
	return strings.TrimSuffix(r.Path, ".json") + ".lock"
}

// withFileLock runs fn while holding the registry's cross-process lock.
// The lock uses the same O_EXCL exclusive-create primitive as ProjectLock,
// with dead-holder detection so a crashed process cannot deadlock the
// registry forever. Steal-based recovery is safe here because the lock is
// held only around the read-modify-write, never across guest operations.
func (r *ProjectRegistry) withFileLock(fn func() error) error {
	lockPath := r.lockPath()
	if err := r.FS.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}
	for {
		err := r.FS.CreateExclusive(lockPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return err
		}
		// A live holder: wait for it to finish.
		data, rerr := r.FS.ReadFile(lockPath)
		if rerr != nil {
			continue // holder released between the failed create and the read
		}
		var pid int
		if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil || !processAlive(pid) {
			_ = r.FS.Remove(lockPath)
			continue
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer func() { _ = r.FS.Remove(lockPath) }()
	return fn()
}

// registryEntry is one serialized registry record.
type registryEntry struct {
	Root     string `json:"root"`
	Instance string `json:"instance"`
}

// registryFile is the on-disk format, versioned like the other managed files.
type registryFile struct {
	SchemaVersion int             `json:"schemaVersion"`
	Entries       []registryEntry `json:"entries"`
}

const registrySchemaVersion = 1

func (r *ProjectRegistry) load() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadLocked()
}

// loadLocked reads the registry file into entries. The caller holds r.mu
// (and, for mutations, the cross-process file lock).
func (r *ProjectRegistry) loadLocked() error {
	if r.loaded {
		return nil
	}
	data, err := r.FS.ReadFile(r.Path)
	if err != nil {
		if os.IsNotExist(err) {
			r.loaded = true
			return nil
		}
		return err
	}
	var rf registryFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return fmt.Errorf("project registry %s: %w", r.Path, err)
	}
	if rf.SchemaVersion > registrySchemaVersion {
		return fmt.Errorf("project registry %s: schemaVersion %d is newer than this build supports (%d)", r.Path, rf.SchemaVersion, registrySchemaVersion)
	}
	r.entries = map[string]string{}
	for _, e := range rf.Entries {
		r.entries[e.Root] = e.Instance
	}
	r.loaded = true
	return nil
}

// Lookup returns the instance name registered for a root, if any.
func (r *ProjectRegistry) Lookup(root string) (string, bool, error) {
	if err := r.load(); err != nil {
		return "", false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	inst, ok := r.entries[root]
	return inst, ok, nil
}

// Register records that root owns instance. It refuses to overwrite a
// different root's claim on the same instance name (collision fail-closed)
// and to rebind a root to a different instance silently: rebinding is an
// explicit operation (Rebind), not a side effect of Register.
//
// The whole check-and-write runs under the cross-process registry lock with
// a fresh reload from disk, so a concurrent registration from another
// process is neither lost nor overwritten.
func (r *ProjectRegistry) Register(root, instance string) error {
	return r.withFileLock(func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		// Reload under the lock: another process may have registered since
		// this object last read the file.
		r.loaded = false
		if err := r.loadLocked(); err != nil {
			return err
		}
		if existing, ok := r.entries[root]; ok && existing != instance {
			return fmt.Errorf("project %s is already registered to instance %s; rebinding to %s requires an explicit rebind", root, existing, instance)
		}
		for otherRoot, otherInst := range r.entries {
			if otherRoot != root && otherInst == instance {
				return fmt.Errorf("instance %s is already registered to project %s; refusing to create a duplicate registration", instance, otherRoot)
			}
		}
		if r.entries[root] == instance {
			return nil
		}
		// Roll back the in-memory entry if the write fails, so a retry on
		// the same object does not report success from the idempotent
		// branch while nothing was persisted.
		r.entries[root] = instance
		if err := r.save(); err != nil {
			delete(r.entries, root)
			return err
		}
		return nil
	})
}

// Rebind explicitly moves a root to a different instance.
func (r *ProjectRegistry) Rebind(root, instance string) error {
	return r.withFileLock(func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.loaded = false
		if err := r.loadLocked(); err != nil {
			return err
		}
		for otherRoot, otherInst := range r.entries {
			if otherRoot != root && otherInst == instance {
				return fmt.Errorf("instance %s is already registered to project %s", instance, otherRoot)
			}
		}
		prev, had := r.entries[root]
		r.entries[root] = instance
		if err := r.save(); err != nil {
			if had {
				r.entries[root] = prev
			} else {
				delete(r.entries, root)
			}
			return err
		}
		return nil
	})
}

// save writes the registry atomically, sorted for stable diffs.
func (r *ProjectRegistry) save() error {
	rf := registryFile{SchemaVersion: registrySchemaVersion}
	roots := make([]string, 0, len(r.entries))
	for root := range r.entries {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	for _, root := range roots {
		rf.Entries = append(rf.Entries, registryEntry{Root: root, Instance: r.entries[root]})
	}
	data, err := json.MarshalIndent(rf, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(r.FS, r.Path, append(data, '\n'), 0o600)
}

// ProjectLock serializes per-project setup mutations so concurrent setup
// calls cannot create duplicate instances. The lock is advisory and
// host-local, held for the duration of a single setup call.
type ProjectLock struct {
	// Path is the lockfile path in host state.
	Path string
	FS   FS
	// WaitTimeout and PollInterval allow bounded, deterministic lock waits.
	// Zero values use conservative defaults.
	WaitTimeout  time.Duration
	PollInterval time.Duration
	// Test seam for OS-specific transient create errors.
	retryableCreateError func(error) bool
}

var projectLockTokenCounter uint64

// Acquire takes the lock, waiting for at most WaitTimeout. It returns a
// release function. Exclusivity is enforced by the OS, not by a check-then-
// write race: the lockfile is created with O_CREATE|O_EXCL, which fails
// atomically when it already exists. Dead owners and abandoned takeover
// markers are reclaimed; live lock holders or unreapable markers produce a
// path/PID error instead of blocking launches indefinitely.
func (l *ProjectLock) Acquire() (release func(), err error) {
	if err := l.FS.MkdirAll(filepath.Dir(l.Path), 0o755); err != nil {
		return nil, err
	}
	waitTimeout := l.WaitTimeout
	if waitTimeout <= 0 {
		waitTimeout = 30 * time.Second
	}
	pollInterval := l.PollInterval
	if pollInterval <= 0 {
		pollInterval = 50 * time.Millisecond
	}
	started := time.Now()
	retryableCreate := l.retryableCreateError
	if retryableCreate == nil {
		retryableCreate = isTransientFilesystemError
	}
	ownerToken := projectLockToken()
	// The steal file proves ownership of a stale-lock removal: two waiters
	// can both observe the same dead PID, and an unconditional Remove would
	// let the second waiter delete the first waiter's freshly created live
	// lock. Only the waiter that wins the steal-create may remove the stale
	// lockfile, and it re-reads the PID immediately before removing, so a
	// replacement lock created in between is never destroyed.
	stealPath := l.Path + ".steal"
	firstAttempt := true
	var lastReadErr error
	for {
		if !firstAttempt && time.Since(started) >= waitTimeout {
			if lastReadErr != nil {
				return nil, lastReadErr
			}
			return nil, projectLockWaitError(l.FS, l.Path, stealPath, waitTimeout)
		}
		firstAttempt = false
		err := l.FS.CreateExclusive(l.Path, ownerToken, 0o600)
		if err == nil {
			return func() { removeProjectLockIfOwned(l.FS, l.Path, ownerToken) }, nil
		}
		if !os.IsExist(err) {
			if retryableCreate(err) {
				lastReadErr = fmt.Errorf("create project lock %s: %w", l.Path, err)
				time.Sleep(pollInterval)
				continue
			}
			return nil, err
		}
		// Stale-lock detection: if the holder is dead, break the lock — but
		// only after winning the steal race for this lockfile.
		data, rerr := l.FS.ReadFile(l.Path)
		if rerr != nil {
			// The holder released between the failed create and this read;
			// retry immediately.
			if os.IsNotExist(rerr) {
				continue
			}
			// A transient read failure — on Windows, an antivirus or
			// indexer handle on the freshly written lockfile can deny
			// read access for a short window — is retried within the
			// bounded wait instead of aborting the acquisition. The last
			// read error is kept: if the deadline expires, it is returned
			// so a permanently unreadable lock path (e.g. a directory)
			// stays diagnosable instead of degrading into a generic
			// timeout.
			lastReadErr = fmt.Errorf("read project lock %s: %w", l.Path, rerr)
			if time.Since(started) < waitTimeout {
				time.Sleep(pollInterval)
				continue
			}
			return nil, lastReadErr
		}
		pid, hasPID := projectLockPID(data)
		if hasPID && processAlive(pid) {
			if time.Since(started) >= waitTimeout {
				return nil, projectLockWaitError(l.FS, l.Path, stealPath, waitTimeout)
			}
			time.Sleep(pollInterval)
			continue
		}
		if !hasPID {
			// A newly created lock is visible just before its PID bytes are
			// written; give that tiny window a second read before treating it
			// as abandoned.
			time.Sleep(20 * time.Millisecond)
			fresh, ferr := l.FS.ReadFile(l.Path)
			if ferr != nil || !bytesEqual(fresh, data) {
				continue
			}
		}
		stealToken := projectLockToken()
		if serr := l.FS.CreateExclusive(stealPath, stealToken, 0o600); serr != nil {
			// Another waiter is already stealing this lock; let it finish.
			if os.IsExist(serr) {
				reapStaleProjectLockMarker(l.FS, stealPath)
				if time.Since(started) >= waitTimeout {
					return nil, projectLockWaitError(l.FS, l.Path, stealPath, waitTimeout)
				}
				time.Sleep(pollInterval)
				continue
			}
			return nil, serr
		}
		// Re-read both ownership files under the steal: if either changed,
		// another waiter has already advanced the lock.
		if !projectLockFileOwned(l.FS, stealPath, stealToken) {
			continue
		}
		fresh, rerr2 := l.FS.ReadFile(l.Path)
		if rerr2 != nil {
			removeProjectLockIfOwned(l.FS, stealPath, stealToken)
			continue
		}
		freshPID, freshHasPID := projectLockPID(fresh)
		if bytesEqual(fresh, data) && (!freshHasPID || !processAlive(freshPID)) && projectLockFileOwned(l.FS, stealPath, stealToken) {
			_ = l.FS.Remove(l.Path)
		}
		removeProjectLockIfOwned(l.FS, stealPath, stealToken)
	}
}

func projectLockWaitError(fs FS, lockPath, stealPath string, waited time.Duration) error {
	if data, err := fs.ReadFile(lockPath); err == nil {
		if pid, ok := projectLockPID(data); ok && processAlive(pid) {
			return fmt.Errorf("timed out after %s waiting for project lock %s held by PID %d", waited, lockPath, pid)
		}
	}
	if data, err := fs.ReadFile(stealPath); err == nil {
		if pid, ok := projectLockPID(data); ok {
			return fmt.Errorf("timed out after %s waiting for project lock %s; takeover marker %s is held by PID %d", waited, lockPath, stealPath, pid)
		}
	}
	return fmt.Errorf("timed out after %s waiting for project lock %s", waited, lockPath)
}

func projectLockToken() []byte {
	sequence := atomic.AddUint64(&projectLockTokenCounter, 1)
	return []byte(fmt.Sprintf("%d %d %d\n", os.Getpid(), time.Now().UnixNano(), sequence))
}

func projectLockPID(data []byte) (int, bool) {
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func projectLockFileOwned(fs FS, path string, token []byte) bool {
	var data []byte
	err := retryFilesystemOperation(func() error {
		var err error
		data, err = fs.ReadFile(path)
		return err
	}, isTransientFilesystemError)
	return err == nil && bytesEqual(data, token)
}

func removeProjectLockIfOwned(fs FS, path string, token []byte) {
	if projectLockFileOwned(fs, path, token) {
		// Removal is retried with backoff: on Windows, antivirus or
		// indexer handles on a freshly written lockfile can deny delete
		// access for a short window. A single ignored failure would leak
		// a lock whose PID still looks alive — processAlive is
		// deliberately conservative on Windows, so the steal path cannot
		// recover it and the project wedges.
		// Ownership is re-checked before each retry: if the file changed
		// hands or vanished between attempts, it is no longer ours to
		// remove.
		delay := 10 * time.Millisecond
		for attempt := 0; ; attempt++ {
			err := fs.Remove(path)
			if err == nil || os.IsNotExist(err) {
				return
			}
			if attempt >= 7 {
				return
			}
			if !projectLockFileOwned(fs, path, token) {
				return
			}
			time.Sleep(delay)
			delay *= 2
		}
	}
}

// reapStaleProjectLockMarker renames an abandoned marker to a unique path
// before deleting it. A concurrent replacement is never removed by pathname.
func reapStaleProjectLockMarker(fs FS, path string) (bool, int) {
	data, err := fs.ReadFile(path)
	if err != nil {
		return false, 0
	}
	pid, hasPID := projectLockPID(data)
	if hasPID && processAlive(pid) {
		return false, pid
	}
	if !hasPID {
		time.Sleep(20 * time.Millisecond)
		fresh, err := fs.ReadFile(path)
		if err != nil || !bytesEqual(fresh, data) {
			return false, 0
		}
		if pid, hasPID = projectLockPID(fresh); hasPID && processAlive(pid) {
			return false, pid
		}
	}
	sequence := atomic.AddUint64(&projectLockTokenCounter, 1)
	quarantine := fmt.Sprintf("%s.stale-%d-%d-%d", path, os.Getpid(), time.Now().UnixNano(), sequence)
	if err := fs.RenameTmp(path, quarantine); err != nil {
		return false, pid
	}
	moved, err := fs.ReadFile(quarantine)
	if err != nil {
		_ = fs.Remove(quarantine)
		return false, pid
	}
	movedPID, movedHasPID := projectLockPID(moved)
	if !bytesEqual(moved, data) && !movedHasPID {
		// A replacement may have been observed between the initial read and
		// rename. Do not discard an ambiguous in-creation marker.
		if err := fs.CreateExclusive(path, moved, 0o600); err == nil {
			_ = fs.Remove(quarantine)
		}
		return false, 0
	}
	if movedHasPID && processAlive(movedPID) {
		// A live marker replaced the stale bytes between the read and rename.
		// Restore it only if the original path is still vacant.
		if err := fs.CreateExclusive(path, moved, 0o600); err == nil {
			_ = fs.Remove(quarantine)
		}
		return false, movedPID
	}
	_ = fs.Remove(quarantine)
	return true, 0
}

// bytesEqual compares two byte slices without pulling in bytes for one use.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// processAlive reports whether a PID exists on this host. On Windows,
// os.FindProcess opens the process, so success there means it is alive.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		// FindProcess opens a process handle on Windows. Release it immediately;
		// PID checks there are intentionally conservative and the bounded lock
		// wait is the backstop for stale/recycled PIDs.
		_ = p.Release()
		return true
	}
	// Signal 0 probes existence without delivering anything.
	return p.Signal(syscall.Signal(0)) == nil
}
