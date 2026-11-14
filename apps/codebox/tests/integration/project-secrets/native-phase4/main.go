package main

// Phase 4 native channel probe (TEST ARTIFACT ONLY).
//
// Qualifies, on native macOS arm64 with the pinned Microsandbox 0.7.6
// runtime and Go SDK, whether a channel exists for handing a guest a
// plaintext value it must consume LOCALLY (codebox Phase 4, issue #151)
// while satisfying all of:
//
//  1. the value never appears in host or guest argv (/proc/*/cmdline, ps);
//  2. the value never appears in logs: host-side, guest, the persisted
//     runtime log, or any runtime journal;
//  3. the value never lands in the PERSISTED runtime configuration
//     (msb.db / saved sandbox config / inspect output);
//  4. the value never exists in a plaintext file on the HOST;
//  5. exactly who inside the guest can read it, from where, and for how
//     long, is measured and stated — not assumed.
//
// Channels under test, each with its own synthetic random canary:
//
//	A   SDK FS().Write onto the disk-backed workspace  (baseline)
//	B   SDK FS().Write onto a RAM-backed tmpfs mount   (primary candidate)
//	E1  SDK ExecStream stdin piped to a tmpfs file     (streaming variant)
//	E2  SDK ExecStream stdin read by a process, memory only
//	C   negative control: value embedded in Shell argv (MUST be caught)
//	D   negative control: inline value at create       (MUST be persisted)
//	F   negative control: per-exec environment value   (MUST be in environ)
//
// Removal, restart and snapshot capture are measured for A and B.
// Only synthetic canaries are used; nothing touches the tester's
// ~/.microsandbox, an installed msb, the trust store, DNS, or interfaces.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	msb "github.com/superradcompany/microsandbox/sdk/go"
)

const source = "bd689185cc8519ff23e5ce4b0c520c621523f43b"
const archiveSHA = "a4f1722fee6460d4e2813e62b60dae4c092021d8120b7aaa9c2f4b5679d97d89"
const kitRev = "p4v1"

// Guest layout. The tmpfs mount is the RAM-backed delivery target; the
// workspace path is the disk-backed baseline. The scanner lives on the
// ordinary root fs; the patterns file lives on the tmpfs so the search
// values themselves never touch the guest disk.
const (
	tmpfsMount   = "/jcram"
	diskDir      = "/workspace/jcp4"
	diskPath     = "/workspace/jcp4/local-secret"
	tmpfsPath    = "/jcram/tmpfs-secret"
	stdinPath    = "/jcram/stdin-secret"
	patternsPath = "/jcram/patterns.json"
	scanDir      = "/jcscan"
	scannerPath  = "/jcscan/scan.py"
	readerPath   = "/jcscan/reader.py"
	leakEnv      = "JC_P4_LEAK" // channel D: inline value at create
	envVarF      = "JC_P4_ENV"  // channel F: per-exec environment
)

// canary IDs
const (
	cA  = "A"  // disk FS().Write
	cB  = "B"  // tmpfs FS().Write
	cC  = "C"  // argv control
	cD  = "D"  // persisted-inline control
	cE1 = "E1" // stdin -> tmpfs file
	cE2 = "E2" // stdin -> process memory
	cF  = "F"  // per-exec environ control
)

var canaryIDs = []string{cA, cB, cC, cD, cE1, cE2, cF}

// hostAllowed maps each canary to the host-file classes it may legitimately
// appear in. Everything else is a violation. Class absence is always
// allowed except where a control REQUIRES a hit (asserted separately).
//
//	A:  the guest's disk is a host file (the runtime's upper image), and a
//	    snapshot copies it — expected, and itself a Phase 4 finding.
//	B/E1/E2: must NEVER exist in any host file.
//	C:  the runtime MAY log exec argv (measured); nothing else allowed.
//	D:  the runtime persists inline values by design (the control); it may
//	    also sit in the disk image if the runtime writes it into the guest.
//	F:  the runtime MAY log exec env (measured); nothing else allowed.
var hostAllowed = map[string]map[string]bool{
	cA:  {"disk-image": true, "snapshot": true},
	cB:  {},
	cE1: {},
	cE2: {},
	cC:  {"log": true},
	cD:  {"store": true, "disk-image": true, "snapshot": true},
	cF:  {"log": true},
}

//go:embed guest
var guestFixtures embed.FS

var failed bool
var sensitive []string
var authorizationRE = regexp.MustCompile(`(?i)(?:proxy-authorization|authorization)[^\r\n]*|(?:basic|bearer)\s+[A-Za-z0-9+/=_\-.]+`)
var validCaseName = regexp.MustCompile(`^[a-z0-9-]+$`)

// longHexRE keeps the runner's results.txt invariant airtight: the only
// 40+ hex runs allowed in the recorded evidence are the two pinned
// constants, and both appear in META lines on stdout, which is never
// scrubbed. In scrubbed diagnostics any other long hex run — a canary
// somehow missed above, a snapshot digest inside an SDK error — becomes a
// marker instead of a false-positive leak (or a real one).
var longHexRE = regexp.MustCompile(`\b[0-9a-f]{40,}\b`)

func scrub(message string) string {
	for _, value := range sensitive {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[REDACTED-CANARY]")
		}
	}
	message = authorizationRE.ReplaceAllString(message, "[REDACTED-AUTHORIZATION]")
	return longHexRE.ReplaceAllStringFunc(message, func(tok string) string {
		if tok == source || tok == archiveSHA {
			return tok
		}
		return "[REDACTED-HEX]"
	})
}

func diagnostic(stage string, err error, detail string) {
	if err == nil && detail == "" {
		return
	}
	kind, code, message := "non-sdk", -1, detail
	if err != nil {
		message = err.Error()
		var sdkError *msb.Error
		if errors.As(err, &sdkError) {
			kind = sdkError.Kind.String()
			code = int(sdkError.Kind)
		}
		if detail != "" {
			message += "; " + detail
		}
	}
	payload, _ := json.Marshal(map[string]any{"stage": stage, "kind": kind, "kind_code": code, "message": scrub(message)})
	fmt.Fprintln(os.Stderr, "DIAGNOSTIC "+string(payload))
}

func registerCanaries(values ...string) {
	for _, v := range values {
		sensitive = append(sensitive, v,
			base64.StdEncoding.EncodeToString([]byte(v)),
			base64.RawStdEncoding.EncodeToString([]byte(v)),
			base64.StdEncoding.EncodeToString([]byte("user:"+v)))
	}
}

func result(name string, ok bool) {
	if !validCaseName.MatchString(name) {
		name = "invalid-case-name"
		ok = false
	}
	status := "PASS"
	if !ok {
		status = "FAIL"
		failed = true
	}
	fmt.Printf("%s %s\n", status, name)
}

func random() string {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		panic("random unavailable")
	}
	return hex.EncodeToString(b)
}

func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// --------------------------------------------------------------------------
// host-side recursive byte scan
// --------------------------------------------------------------------------

type hostHit struct {
	Canary string `json:"canary"`
	Path   string `json:"path"`
	Class  string `json:"class"`
}

// classify buckets a run-dir-relative path. The disk image of the guest is
// the runtime's "upper"; snapshots copy it; the persisted registry/config is
// the "store"; anything named like a log is a log. Class names are coarse on
// purpose: the assertion is about WHERE a value may exist at all.
func classify(rel string) string {
	lower := strings.ToLower(filepath.ToSlash(rel))
	switch {
	case strings.Contains(lower, "snapshot"):
		return "snapshot"
	case strings.Contains(lower, "upper") || strings.Contains(lower, "rootfs") ||
		strings.Contains(lower, ".img") || strings.Contains(lower, "disk"):
		return "disk-image"
	case strings.Contains(lower, "log") || strings.Contains(lower, "journal"):
		return "log"
	case strings.Contains(lower, ".db") || strings.Contains(lower, "config") ||
		strings.Contains(lower, "registry") || strings.HasSuffix(lower, ".json"):
		return "store"
	default:
		return "other"
	}
}

// scanHost streams every regular file under root looking for the canary
// bytes (raw and base64 forms). Chunked with overlap so a value split
// across a read boundary is still found. Non-regular files are skipped.
func scanHost(root string, canaries map[string]string) []hostHit {
	type needle struct {
		id       string
		raw, b64 []byte
	}
	var needles []needle
	for _, id := range canaryIDs {
		v := canaries[id]
		needles = append(needles, needle{id, []byte(v), []byte(base64.StdEncoding.EncodeToString([]byte(v)))})
	}
	var hits []hostHit
	const chunk = 4 << 20
	const overlap = 256
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || !info.Mode().IsRegular() {
			return nil
		}
		f, e := os.Open(path)
		if e != nil {
			return nil
		}
		defer f.Close()
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return nil
		}
		buf := make([]byte, chunk+overlap)
		var tail int
		seen := map[string]bool{}
		for {
			n, rerr := f.Read(buf[tail : tail+chunk])
			window := buf[:tail+n]
			for _, nd := range needles {
				if seen[nd.id] {
					continue
				}
				if bytes.Contains(window, nd.raw) || bytes.Contains(window, nd.b64) {
					seen[nd.id] = true
					hits = append(hits, hostHit{Canary: nd.id, Path: rel, Class: classify(rel)})
				}
			}
			if rerr != nil || len(seen) == len(needles) {
				break
			}
			tail = len(window)
			if tail > overlap {
				tail = overlap
			}
			copy(buf, window[len(window)-tail:])
		}
		return nil
	})
	return hits
}

func hostViolations(canary string, hits []hostHit) []hostHit {
	var out []hostHit
	for _, h := range hits {
		if h.Canary == canary && !hostAllowed[canary][h.Class] {
			out = append(out, h)
		}
	}
	return out
}

func hostHasClass(canary, class string, hits []hostHit) bool {
	for _, h := range hits {
		if h.Canary == canary && h.Class == class {
			return true
		}
	}
	return false
}

func summarizeHits(stage string, hits []hostHit) {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Canary != hits[j].Canary {
			return hits[i].Canary < hits[j].Canary
		}
		return hits[i].Path < hits[j].Path
	})
	payload, _ := json.Marshal(map[string]any{"hits": hits, "count": len(hits)})
	diagnostic(stage, nil, "host byte scan (locations only, never values): "+string(payload))
}

// --------------------------------------------------------------------------
// guest scanner output
// --------------------------------------------------------------------------

type scanLine struct {
	Pattern string   `json:"pattern"`
	Cmdline []int    `json:"cmdline"`
	Environ []int    `json:"environ"`
	Files   []string `json:"files"`
}

func parseScanLines(out string) map[string]scanLine {
	lines := map[string]scanLine{}
	for _, raw := range strings.Split(out, "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		var sl scanLine
		if json.Unmarshal([]byte(raw), &sl) == nil && sl.Pattern != "" {
			lines[sl.Pattern] = sl
		}
	}
	return lines
}

// --------------------------------------------------------------------------
// probe
// --------------------------------------------------------------------------

func connect(ctx context.Context, name string) (*msb.Sandbox, error) {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return nil, err
	}
	return h.Connect(ctx)
}

func run() {
	dir := flag.String("dir", "", "owned run directory")
	cleanup := flag.Bool("cleanup", false, "cleanup own VM from owned directory")
	flag.Parse()
	home := os.Getenv("MSB_HOME")
	if *dir == "" || home != filepath.Join(*dir, "msb-home") || os.Getenv("MSB_PATH") != filepath.Join(*dir, "runtime", "msb") {
		result("isolated-runtime-preflight", false)
		return
	}
	if *cleanup {
		cleanupOwned(*dir)
		return
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		result("native-darwin-arm64-required", false)
		return
	}
	fmt.Printf("META kit=%s source=%s sdk=0.7.6 runtime=0.7.6 archive_sha256=%s go=%s\n", kitRev, source, archiveSHA, runtime.Version())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	name := "cb-p4-" + random()[:16]
	if e := os.WriteFile(filepath.Join(*dir, "vm-name"), []byte(name), 0600); e != nil {
		diagnostic("ownership-file", e, "")
		result("ownership-file", false)
		return
	}
	restoredName := "cb-p4-r-" + random()[:14]
	// Written BEFORE the restore is attempted so a signal mid-restore still
	// cleans up the partially created sibling VM.
	if e := os.WriteFile(filepath.Join(*dir, "vm-name-restored"), []byte(restoredName), 0600); e != nil {
		diagnostic("ownership-file-restored", e, "")
	}
	defer func() {
		c, done := context.WithTimeout(context.Background(), 60*time.Second)
		defer done()
		ok := removeIfOurs(c, name) && removeIfOurs(c, restoredName)
		removeSnapshotIfOurs(c, *dir, name)
		result("cleanup-own-vm", ok)
	}()

	// Seven synthetic random canaries, one per channel/control. They exist
	// only in this process's memory, the guest, and (for the channels that
	// fail the criteria) the locations this probe names in its diagnostics.
	canaries := map[string]string{}
	for _, id := range canaryIDs {
		canaries[id] = random()
	}
	values := make([]string, 0, len(canaryIDs))
	for _, id := range canaryIDs {
		values = append(values, canaries[id])
	}
	registerCanaries(values...)

	// ONE VM: managed (host-file-backed) root disk kept small for snapshot
	// speed, a RAM-backed tmpfs mount as the primary candidate location,
	// and the channel-D inline secret whose persisted presence is the
	// control proving the persisted-config scan works.
	s, e := msb.CreateSandbox(ctx, name,
		msb.WithImage("docker.io/library/python:3.12-alpine"),
		msb.WithCPUs(2), msb.WithMemory(768), msb.WithDetached(),
		msb.WithMaxDuration(16*time.Minute),
		msb.WithRootDisk(msb.RootDisk.Managed(256)),
		msb.WithEntrypoint("/bin/sh", "-c"), msb.WithCmd("sleep 900"),
		msb.WithMounts(map[string]msb.MountConfig{
			tmpfsMount: msb.Mount.Tmpfs(msb.TmpfsOptions{SizeMiB: 8, Noexec: true, Nosuid: true, Nodev: true}),
		}),
		msb.WithSecrets(msb.Secret.Env(leakEnv, canaries[cD], msb.SecretEnvOptions{Allow: []string{"p4.invalid"}})),
	)
	diagnostic("create-vm", e, "")
	result("create-vm", e == nil)
	if e != nil {
		return
	}

	execCtx := func(timeout time.Duration, cmd string, args []string, opts ...msb.ExecOption) (*msb.ExecOutput, error) {
		c, d := context.WithTimeout(ctx, timeout+10*time.Second)
		defer d()
		out, err := s.Exec(c, cmd, args, append(opts, msb.WithExecTimeout(timeout))...)
		if err != nil {
			diagnostic("guest-exec", err, "cmd="+cmd)
		}
		return out, err
	}
	fsCtx := func(timeout time.Duration, call func(context.Context) error) error {
		c, d := context.WithTimeout(ctx, timeout)
		defer d()
		return call(c)
	}
	// guestDigest proves a guest PROCESS can read the file without the
	// value ever crossing exec stdout (only its digest does).
	guestDigest := func(path string, opts ...msb.ExecOption) (string, bool) {
		out, err := execCtx(20*time.Second, "sha256sum", []string{path}, opts...)
		if err != nil || out == nil || !out.Success() {
			return "", false
		}
		fields := strings.Fields(out.Stdout())
		if len(fields) == 0 {
			return "", false
		}
		return fields[0], true
	}
	fsRead := func(path string) (string, bool) {
		var data []byte
		err := fsCtx(30*time.Second, func(c context.Context) error {
			var e2 error
			data, e2 = s.FS().Read(c, path)
			return e2
		})
		if err != nil {
			diagnostic("guest-fs-read", err, path)
			return "", false
		}
		return string(data), true
	}
	fsExists := func(sb *msb.Sandbox, path string) bool {
		c, d := context.WithTimeout(ctx, 30*time.Second)
		defer d()
		ok, err := sb.FS().Exists(c, path)
		if err != nil {
			diagnostic("guest-fs-exists", err, path)
		}
		return ok
	}
	fsStatMode := func(path string) (uint32, bool) {
		c, d := context.WithTimeout(ctx, 30*time.Second)
		defer d()
		st, err := s.FS().Stat(c, path)
		if err != nil {
			diagnostic("guest-fs-stat", err, path)
			return 0, false
		}
		return st.Mode & 0777, true
	}

	// The tmpfs mount must actually be RAM-backed; /proc/mounts says so.
	out, e := execCtx(20*time.Second, "cat", []string{"/proc/mounts"})
	mounted := e == nil && out != nil && out.Success() &&
		regexp.MustCompile(`(?m)^tmpfs `+tmpfsMount+` tmpfs`).MatchString(out.Stdout())
	result("tmpfs-mount-present", mounted)

	// --- fixtures --------------------------------------------------------
	push := func(guestPath, embedPath string) bool {
		data, err := guestFixtures.ReadFile(embedPath)
		if err != nil {
			diagnostic("push-read-embed", err, embedPath)
			return false
		}
		if err := fsCtx(30*time.Second, func(c context.Context) error { return s.FS().Write(c, guestPath, data) }); err != nil {
			diagnostic("push-write", err, guestPath)
			return false
		}
		return true
	}
	fixturesOK := fsCtx(30*time.Second, func(c context.Context) error { return s.FS().Mkdir(c, scanDir) }) == nil &&
		fsCtx(30*time.Second, func(c context.Context) error { return s.FS().Mkdir(c, diskDir) }) == nil &&
		push(scannerPath, "guest/scanner.py") && push(readerPath, "guest/reader.py")
	result("guest-fixtures-pushed", fixturesOK)
	if !fixturesOK {
		return
	}

	// --- channel A: FS().Write to the disk-backed workspace --------------
	werrA := fsCtx(30*time.Second, func(c context.Context) error { return s.FS().Write(c, diskPath, []byte(canaries[cA])) })
	readA, okA := fsRead(diskPath)
	digestA, procA := guestDigest(diskPath)
	result("deliver-disk-write", werrA == nil && okA && readA == canaries[cA] && procA && digestA == digest(canaries[cA]))

	// --- channel B: FS().Write to the tmpfs mount ------------------------
	werrB := fsCtx(30*time.Second, func(c context.Context) error { return s.FS().Write(c, tmpfsPath, []byte(canaries[cB])) })
	readB, okB := fsRead(tmpfsPath)
	digestB, procB := guestDigest(tmpfsPath)
	result("deliver-tmpfs-write", werrB == nil && okB && readB == canaries[cB] && procB && digestB == digest(canaries[cB]))

	// --- channel E1: ExecStream stdin -> tmpfs file ----------------------
	e1OK := func() bool {
		c, d := context.WithTimeout(ctx, 45*time.Second)
		defer d()
		h, err := s.ExecStream(c, "/bin/sh", []string{"-c", "cat > " + stdinPath}, msb.WithExecStdinPipe(), msb.WithExecTimeout(30*time.Second))
		if err != nil {
			diagnostic("stdin-file-start", err, "")
			return false
		}
		defer func() { _ = h.Close() }()
		sink := h.TakeStdin()
		if sink == nil {
			diagnostic("stdin-file-sink", nil, "TakeStdin returned nil")
			return false
		}
		if _, err := sink.Write([]byte(canaries[cE1])); err != nil {
			diagnostic("stdin-file-write", err, "")
			return false
		}
		if err := sink.Close(); err != nil {
			diagnostic("stdin-file-close", err, "")
			return false
		}
		code, err := h.Wait(c)
		if err != nil || code != 0 {
			diagnostic("stdin-file-wait", err, fmt.Sprintf("exit=%d", code))
			return false
		}
		return true
	}()
	readE1, okE1 := fsRead(stdinPath)
	digestE1, procE1 := guestDigest(stdinPath)
	result("deliver-stdin-file", e1OK && okE1 && readE1 == canaries[cE1] && procE1 && digestE1 == digest(canaries[cE1]))

	// --- channel E2: ExecStream stdin -> process memory only -------------
	e2Digest := ""
	e2OK := func() bool {
		c, d := context.WithTimeout(ctx, 45*time.Second)
		defer d()
		h, err := s.ExecStream(c, "python3", []string{readerPath}, msb.WithExecStdinPipe(), msb.WithExecTimeout(30*time.Second))
		if err != nil {
			diagnostic("stdin-memory-start", err, "")
			return false
		}
		defer func() { _ = h.Close() }()
		sink := h.TakeStdin()
		if sink == nil {
			diagnostic("stdin-memory-sink", nil, "TakeStdin returned nil")
			return false
		}
		if _, err := sink.Write([]byte(canaries[cE2])); err != nil {
			diagnostic("stdin-memory-write", err, "")
			return false
		}
		if err := sink.Close(); err != nil {
			diagnostic("stdin-memory-close", err, "")
			return false
		}
		out, err := h.Collect(c)
		if err != nil || out == nil || !out.Success() {
			diagnostic("stdin-memory-collect", err, "")
			return false
		}
		e2Digest = strings.TrimSpace(out.Stdout())
		return true
	}()
	result("deliver-stdin-memory", e2OK && e2Digest == digest(canaries[cE2]))

	// --- criterion 5: who can read, measured -----------------------------
	modeA, statA := fsStatMode(diskPath)
	modeB, statB := fsStatMode(tmpfsPath)
	if statA && statB {
		diagnostic("delivery-modes", nil, fmt.Sprintf("as-delivered modes: disk=%04o tmpfs=%04o", modeA, modeB))
	}
	nobody := msb.WithExecUser("65534")
	_, preA := guestDigest(diskPath, nobody)
	_, preB := guestDigest(tmpfsPath, nobody)
	diagnostic("prechmod-nobody-read", nil, fmt.Sprintf("non-root could read before chmod: disk=%v tmpfs=%v", preA, preB))
	chmod, e := execCtx(20*time.Second, "chmod", []string{"0600", diskPath, tmpfsPath, stdinPath})
	modeA2, statA2 := fsStatMode(diskPath)
	modeB2, statB2 := fsStatMode(tmpfsPath)
	_, postA := guestDigest(diskPath, nobody)
	_, postB := guestDigest(tmpfsPath, nobody)
	digestA2, rootA := guestDigest(diskPath)
	digestB2, rootB := guestDigest(tmpfsPath)
	result("chmod-0600-achievable", e == nil && chmod != nil && chmod.Success() &&
		statA2 && statB2 && modeA2 == 0600 && modeB2 == 0600 &&
		!postA && !postB && rootA && rootB &&
		digestA2 == digest(canaries[cA]) && digestB2 == digest(canaries[cB]))
	// Rotation design fact: does rewriting via FS().Write keep the mode?
	werrB2 := fsCtx(30*time.Second, func(c context.Context) error { return s.FS().Write(c, tmpfsPath, []byte(canaries[cB])) })
	modeB3, statB3 := fsStatMode(tmpfsPath)
	if werrB2 == nil && statB3 {
		diagnostic("rewrite-mode", nil, fmt.Sprintf("mode after FS().Write rewrite of a 0600 file: %04o", modeB3))
	}

	// --- guest directory hygiene ------------------------------------------
	listExact := func(path string, want ...string) bool {
		c, d := context.WithTimeout(ctx, 30*time.Second)
		defer d()
		entries, err := s.FS().List(c, path)
		if err != nil {
			diagnostic("guest-fs-list", err, path)
			return false
		}
		var got []string
		for _, en := range entries {
			got = append(got, filepath.Base(en.Path))
		}
		sort.Strings(got)
		sort.Strings(want)
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	result("guest-dirs-no-leftovers",
		listExact(diskDir, "local-secret") &&
			listExact(tmpfsMount, "stdin-secret", "tmpfs-secret") &&
			listExact(scanDir, "reader.py", "scan.py"))

	// --- negative controls -------------------------------------------------
	// C: the value rides inside the shell's argv for five minutes. The
	// command is deliberately compound: a bare `sh -c "sleep 300"` is exec
	// optimized into sleep, which strips the canary from the process table and
	// would make every argv assertion below unfalsifiable.
	cCtl, e := s.ExecStream(ctx, "/bin/sh", []string{"-c", "sleep 300; true # jcp4argv-" + canaries[cC]}, msb.WithExecTimeout(320*time.Second))
	diagnostic("argv-control-start", e, "")
	// F: the value rides in a per-exec environment variable.
	fCtl, e2 := s.ExecStream(ctx, "sleep", []string{"300"}, msb.WithExecEnv(map[string]string{envVarF: canaries[cF]}), msb.WithExecTimeout(320*time.Second))
	diagnostic("env-control-start", e2, "")
	if e != nil || e2 != nil || cCtl == nil || fCtl == nil {
		diagnostic("controls-start", nil, "a negative-control process failed to start; the argv/environ scan cases will fail closed")
	}
	defer func() {
		if cCtl != nil {
			c, d := context.WithTimeout(context.Background(), 15*time.Second)
			defer d()
			_ = cCtl.Kill(c)
			_ = cCtl.Close()
		}
		if fCtl != nil {
			c, d := context.WithTimeout(context.Background(), 15*time.Second)
			defer d()
			_ = fCtl.Kill(c)
			_ = fCtl.Close()
		}
	}()

	// --- guest-side scan: /proc cmdline + environ, /var/log ---------------
	// The patterns travel to the scanner through the tmpfs file (0600), so
	// the scanner's own argv/environ carry no value and cannot self-match.
	patternDoc, _ := json.Marshal(map[string]any{"patterns": canaries})
	patternsOK := fsCtx(30*time.Second, func(c context.Context) error { return s.FS().Write(c, patternsPath, patternDoc) }) == nil
	if patternsOK {
		_, _ = execCtx(20*time.Second, "chmod", []string{"0600", patternsPath})
	}
	scanOut, scanErr := execCtx(90*time.Second, "python3", []string{scannerPath, patternsPath})
	_ = fsCtx(30*time.Second, func(c context.Context) error { return s.FS().Remove(c, patternsPath) })
	scan := map[string]scanLine{}
	if patternsOK && scanErr == nil && scanOut != nil && scanOut.Success() {
		scan = parseScanLines(scanOut.Stdout())
	} else {
		diagnostic("guest-scan-run", scanErr, "scanner did not complete")
	}
	got := func(id string) scanLine { return scan[id] }
	scanned := len(scan) == len(canaryIDs)
	result("argv-scan-finds-control", scanned && len(got(cC).Cmdline) > 0)
	result("environ-scan-finds-control", scanned && len(got(cF).Environ) > 0)
	argvClean := scanned
	for _, id := range []string{cA, cB, cD, cE1, cE2, cF} {
		argvClean = argvClean && len(got(id).Cmdline) == 0
	}
	result("argv-clean-for-delivered", argvClean)
	environClean := scanned
	for _, id := range []string{cA, cB, cC, cD, cE1, cE2} {
		environClean = environClean && len(got(id).Environ) == 0
	}
	result("environ-clean-for-delivered", environClean)
	varLogClean := scanned
	for _, id := range canaryIDs {
		varLogClean = varLogClean && len(got(id).Files) == 0
	}
	result("guest-varlog-clean", varLogClean)
	// The tmpfs held only the delivery files plus the (now removed)
	// patterns file: anything else is a leftover.
	result("guest-tmpfs-no-leftovers", listExact(tmpfsMount, "stdin-secret", "tmpfs-secret"))

	// --- criterion 1 host side: no canary in any host process argv -------
	hostArgvClean := true
	if ps, psErr := exec.Command("ps", "-axo", "args").Output(); psErr != nil {
		diagnostic("host-ps", psErr, "")
		hostArgvClean = false
	} else {
		for _, id := range canaryIDs {
			if strings.Contains(string(ps), canaries[id]) {
				hostArgvClean = false
				diagnostic("host-argv-leak", nil, "canary "+id+" present in host process argv")
			}
		}
	}
	result("host-argv-clean", hostArgvClean)

	// macOS unified log: the FFI runtime lives in THIS process, so anything
	// it sends to os_log is recorded under process "probe". Bounded to the
	// delivery window. Apple's private-data redaction may mask a value
	// behind <private>, so a clean result here is necessary-but-weaker
	// evidence; see README "What this kit cannot prove".
	osLogClean := true
	func() {
		c, d := context.WithTimeout(ctx, 150*time.Second)
		defer d()
		lg, lgErr := exec.CommandContext(c, "/usr/bin/log", "show", "--last", "10m", "--style", "compact",
			"--predicate", "process == \"probe\"").Output()
		if lgErr != nil {
			diagnostic("host-oslog", lgErr, "log show failed; unified-log surface untested")
			osLogClean = false
			return
		}
		for _, id := range canaryIDs {
			if bytes.Contains(lg, []byte(canaries[id])) {
				osLogClean = false
				diagnostic("host-oslog-leak", nil, "canary "+id+" present in the macOS unified log under the probe process")
			}
		}
	}()
	result("host-oslog-clean", osLogClean)

	// --- criterion 2: the persisted runtime log ---------------------------
	logText := ""
	func() {
		c, d := context.WithTimeout(ctx, 45*time.Second)
		defer d()
		entries, err := s.Logs(c, msb.LogOptions{Sources: []msb.LogSource{
			msb.LogSourceStdout, msb.LogSourceStderr, msb.LogSourceOutput, msb.LogSourceSystem,
		}})
		if err != nil {
			diagnostic("runtime-logs-read", err, "")
			return
		}
		for _, en := range entries {
			logText += en.Text() + "\n"
		}
	}()
	if logText == "" {
		diagnostic("runtime-logs-empty", nil, "persisted-log query returned no entries; the host file scan is the authoritative log surface for this run")
	}
	runtimeLogClean := true
	for _, id := range []string{cA, cB, cD, cE1, cE2, cF} {
		if strings.Contains(logText, canaries[id]) {
			runtimeLogClean = false
			diagnostic("runtime-log-leak", nil, "canary "+id+" present in persisted runtime log")
		}
	}
	result("runtime-log-clean-for-delivered", runtimeLogClean)
	if strings.Contains(logText, canaries[cC]) {
		diagnostic("runtime-log-captures-argv", nil,
			"the argv control DOES appear in the persisted runtime log: channel C also fails criterion 2")
	}
	if strings.Contains(logText, canaries[cF]) {
		diagnostic("runtime-log-captures-env", nil,
			"the per-exec env control DOES appear in the persisted runtime log: channel F also fails criterion 2")
	}

	// --- criterion 3: the persisted runtime configuration -----------------
	rawCfg := ""
	func() {
		c, d := context.WithTimeout(ctx, 30*time.Second)
		defer d()
		fresh, err := msb.GetSandbox(c, name)
		if err != nil {
			diagnostic("persisted-config-get", err, "")
			return
		}
		rawCfg = fresh.ConfigJSON()
	}()
	result("persisted-config-holds-control", rawCfg != "" && strings.Contains(rawCfg, canaries[cD]))
	persistedClean := rawCfg != ""
	for _, id := range []string{cA, cB, cC, cE1, cE2, cF} {
		persistedClean = persistedClean && !strings.Contains(rawCfg, canaries[id])
	}
	result("persisted-config-clean-for-delivered", persistedClean)

	// --- criteria 2/3/4 host side: recursive byte scan of the run dir -----
	// Flush the guest's page cache first so the disk-backed value has had
	// every chance to reach the host-side image; what is NOT there after a
	// sync was never written through.
	_, _ = execCtx(20*time.Second, "sync", nil)
	hits1 := scanHost(*dir, canaries)
	summarizeHits("host-scan-before-snapshot", hits1)
	viol1 := false
	for _, id := range canaryIDs {
		for _, h := range hostViolations(id, hits1) {
			viol1 = true
			diagnostic("host-scan-violation", nil, "canary "+h.Canary+" in disallowed host location class "+h.Class+" at "+h.Path)
		}
	}
	result("no-host-exposure-outside-declared-classes", !viol1)
	result("host-persisted-store-shows-control", hostHasClass(cD, "store", hits1))
	// The disk-backed channel is EXPECTED to expose its value on the host (the
	// guest disk is a host file). Asserting the observation keeps the finding
	// honest: if it stops appearing, the detector is broken, not the exposure.
	result("disk-channel-host-exposure-detected", hostHasClass(cA, "disk-image", hits1) || hostHasClass(cA, "snapshot", hits1))
	if hostHasClass(cA, "disk-image", hits1) {
		diagnostic("disk-value-on-host", nil,
			"channel A value is present in the host-side guest disk image: disk-backed delivery fails a strict reading of criterion 4 while the VM exists")
	}
	if strings.Contains(logText, canaries[cE1]) || strings.Contains(logText, canaries[cE2]) {
		diagnostic("stdin-logged", nil, "the runtime persisted stdin payload bytes: the stdin channel fails criterion 2")
	}

	// --- removal, then restart --------------------------------------------
	rmErr := fsCtx(30*time.Second, func(c context.Context) error { return s.FS().Remove(c, diskPath) })
	result("removal-reported-gone", rmErr == nil && !fsExists(s, diskPath))
	var h *msb.SandboxHandle
	func() {
		c, d := context.WithTimeout(ctx, 30*time.Second)
		defer d()
		h, e = msb.GetSandbox(c, name)
	}()
	if e != nil {
		diagnostic("get-handle", e, "")
		result("get-handle", false)
		return
	}
	s2, e := func() (*msb.Sandbox, error) {
		c, d := context.WithTimeout(ctx, 4*time.Minute)
		defer d()
		return h.Restart(c, msb.WithRestartDetached())
	}()
	diagnostic("restart", e, "")
	result("restart-succeeded", e == nil && s2 != nil)
	if e != nil || s2 == nil {
		return
	}
	s = s2
	result("removal-survives-restart", !fsExists(s, diskPath))
	tmpfsAfterRestart := fsExists(s, tmpfsPath) || fsExists(s, stdinPath)
	result("tmpfs-cleared-on-restart", !tmpfsAfterRestart)
	out, e = execCtx(20*time.Second, "cat", []string{"/proc/mounts"})
	result("tmpfs-mount-persists-across-restart", e == nil && out != nil && out.Success() &&
		regexp.MustCompile(`(?m)^tmpfs `+tmpfsMount+` tmpfs`).MatchString(out.Stdout()))

	// --- re-deliver, stop, snapshot, restore --------------------------------
	werrA3 := fsCtx(30*time.Second, func(c context.Context) error { return s.FS().Write(c, diskPath, []byte(canaries[cA])) })
	werrB3 := fsCtx(30*time.Second, func(c context.Context) error { return s.FS().Write(c, tmpfsPath, []byte(canaries[cB])) })
	_, _ = execCtx(20*time.Second, "sync", nil)
	diagnostic("redeliver-for-snapshot", nil, fmt.Sprintf("disk=%v tmpfs=%v", werrA3 == nil, werrB3 == nil))
	stopErr := func() error {
		c, d := context.WithTimeout(ctx, 90*time.Second)
		defer d()
		return s.Stop(c)
	}()
	diagnostic("stop-for-snapshot", stopErr, "")
	_ = s.Close()

	snapName := "cbp4snap-" + random()[:10]
	selector := name + ":" + snapName
	_ = os.WriteFile(filepath.Join(*dir, "snapshot-name"), []byte(selector), 0600)
	var artifact *msb.SnapshotArtifact
	func() {
		c, d := context.WithTimeout(ctx, 5*time.Minute)
		defer d()
		var serr error
		artifact, serr = h.Snapshot(c, snapName)
		diagnostic("snapshot-created", serr, "")
		e = serr
	}()
	result("snapshot-created", e == nil && artifact != nil)

	// Post-snapshot host scan: tmpfs values must STILL be nowhere on the
	// host; the disk value may now also sit in the snapshot (measured).
	hits2 := scanHost(*dir, canaries)
	summarizeHits("host-scan-after-snapshot", hits2)
	tmpfsOnHost := false
	for _, id := range []string{cB, cE1, cE2} {
		for _, h := range append(append([]hostHit{}, hits1...), hits2...) {
			if h.Canary == id {
				tmpfsOnHost = true
				diagnostic("tmpfs-on-host", nil, "canary "+id+" found in host location class "+h.Class+" at "+h.Path)
			}
		}
	}
	result("tmpfs-never-on-host", !tmpfsOnHost)
	diagnostic("snapshot-captures-disk-value", nil,
		fmt.Sprintf("channel A value present in host-side snapshot bytes: %v", hostHasClass(cA, "snapshot", hits2)))

	if artifact != nil {
		var fork *msb.Sandbox
		func() {
			c, d := context.WithTimeout(ctx, 5*time.Minute)
			defer d()
			var rerr error
			fork, rerr = msb.RestoreSandbox(c, artifact, restoredName)
			diagnostic("snapshot-restore", rerr, "")
			e = rerr
		}()
		result("snapshot-restored", e == nil && fork != nil)
		if fork != nil {
			// The value was re-delivered just before the snapshot, so a
			// restored VM holding it means a snapshot captures a delivery
			// that was live at snapshot time (a copy that no later in-guest
			// deletion can revoke). Absence means the runtime did not flush
			// the write into the snapshot — also reported plainly.
			var restoredDigest string
			var restoredOK bool
			func() {
				c, d := context.WithTimeout(ctx, 45*time.Second)
				defer d()
				out, rerr := fork.Exec(c, "sha256sum", []string{diskPath}, msb.WithExecTimeout(30*time.Second))
				if rerr == nil && out != nil && out.Success() {
					fields := strings.Fields(out.Stdout())
					if len(fields) > 0 {
						restoredDigest, restoredOK = fields[0], true
					}
				}
			}()
			diskValueInSnapshot := restoredOK && restoredDigest == digest(canaries[cA])
			diagnostic("snapshot-captures-delivered-value", nil,
				fmt.Sprintf("value delivered to disk present in a VM restored from that snapshot: %v", diskValueInSnapshot))
			result("snapshot-excludes-tmpfs", !fsExists(fork, tmpfsPath) && !fsExists(fork, stdinPath))
			c, d := context.WithTimeout(ctx, 60*time.Second)
			_ = fork.Stop(c)
			_ = fork.Close()
			d()
		}
	} else {
		result("snapshot-restored", false)
		result("snapshot-excludes-tmpfs", false)
	}
	removeSnapshotIfOurs(ctx, *dir, name)

	// --- teardown: value lifecycle after the VMs are gone ------------------
	func() {
		c, d := context.WithTimeout(context.Background(), 60*time.Second)
		defer d()
		removeIfOurs(c, name)
		removeIfOurs(c, restoredName)
	}()
	hits3 := scanHost(*dir, canaries)
	summarizeHits("host-scan-after-teardown", hits3)
	teardownClean := true
	for _, id := range []string{cB, cE1, cE2} {
		if len(hits3For(id, hits3)) > 0 {
			teardownClean = false
			diagnostic("teardown-violation", nil, "memory/tmpfs canary "+id+" found on host after teardown")
		}
	}
	for _, id := range []string{cC, cF} {
		for _, h := range hostViolations(id, hits3) {
			teardownClean = false
			diagnostic("teardown-violation", nil, "canary "+h.Canary+" in disallowed class "+h.Class+" at "+h.Path)
		}
	}
	result("host-files-clean-after-teardown", teardownClean)
	diskGone := true
	for _, h := range hits3 {
		if h.Canary == cA {
			diskGone = false
			diagnostic("disk-value-outlives-vm", nil, "channel A value still on host after VM removal at "+h.Path)
		}
	}
	result("disk-value-removed-with-vm", diskGone)
	if hostHasClass(cD, "store", hits3) {
		diagnostic("control-outlives-vm", nil,
			"channel D inline value remains in the persisted store even after sandbox removal: inline persistence is sticky")
	}
}

func hits3For(canary string, hits []hostHit) []hostHit {
	var out []hostHit
	for _, h := range hits {
		if h.Canary == canary {
			out = append(out, h)
		}
	}
	return out
}

// removeIfOurs kills and removes a sandbox ONLY if its name carries this
// kit's ownership prefix. Returns false on a real error; a missing sandbox
// is success.
func removeIfOurs(ctx context.Context, name string) bool {
	if !strings.HasPrefix(name, "cb-p4-") {
		diagnostic("cleanup-ownership", nil, "refusing name without cb-p4- prefix")
		return false
	}
	h, e := msb.GetSandbox(ctx, name)
	if e != nil {
		if msb.IsKind(e, msb.ErrSandboxNotFound) {
			return true
		}
		diagnostic("cleanup-get", e, "")
		return false
	}
	diagnostic("cleanup-kill", h.Kill(ctx), "")
	e = msb.RemoveSandbox(ctx, name)
	if e != nil && !msb.IsKind(e, msb.ErrSandboxNotFound) {
		diagnostic("cleanup-remove", e, "")
		return false
	}
	return true
}

// removeSnapshotIfOurs removes the snapshot recorded in the run directory,
// best-effort: the run directory itself is deleted by the runner anyway.
func removeSnapshotIfOurs(ctx context.Context, dir, mainName string) {
	b, e := os.ReadFile(filepath.Join(dir, "snapshot-name"))
	if e != nil {
		return
	}
	selector := strings.TrimSpace(string(b))
	if !strings.HasPrefix(selector, mainName+":cbp4snap-") {
		diagnostic("cleanup-snapshot-ownership", nil, "refusing unexpected snapshot selector")
		return
	}
	c, d := context.WithTimeout(ctx, 60*time.Second)
	defer d()
	err := msb.Snapshot.Remove(c, selector, true)
	switch {
	case err == nil:
		// The selector record is dropped so the runner's later emergency
		// cleanup does not retry a snapshot that is already gone and report a
		// misleading SnapshotNotFound diagnostic.
		_ = os.Remove(filepath.Join(dir, "snapshot-name"))
	case msb.IsKind(err, msb.ErrSnapshotNotFound):
		// Already removed (by the run, or by an earlier cleanup): expected.
	default:
		diagnostic("cleanup-snapshot", err, "")
	}
}

func cleanupOwned(dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, file := range []string{"vm-name", "vm-name-restored"} {
		b, e := os.ReadFile(filepath.Join(dir, file))
		if e != nil {
			continue
		}
		name := strings.TrimSpace(string(b))
		if !strings.HasPrefix(name, "cb-p4-") {
			result("cleanup-ownership", false)
			continue
		}
		h, e := msb.GetSandbox(ctx, name)
		if e == nil {
			diagnostic("cleanup-kill", h.Kill(ctx), "")
			e = msb.RemoveSandbox(ctx, name)
		}
		if !msb.IsKind(e, msb.ErrSandboxNotFound) {
			diagnostic("cleanup-get-or-remove", e, "")
		}
	}
	b, e := os.ReadFile(filepath.Join(dir, "snapshot-name"))
	if e == nil {
		selector := strings.TrimSpace(string(b))
		parts := strings.SplitN(selector, ":", 2)
		if len(parts) == 2 && strings.HasPrefix(parts[0], "cb-p4-") && strings.HasPrefix(parts[1], "cbp4snap-") {
			if err := msb.Snapshot.Remove(ctx, selector, true); err != nil && !msb.IsKind(err, msb.ErrSnapshotNotFound) {
				diagnostic("cleanup-snapshot", err, "")
			}
		}
	}
	result("cleanup", true)
}

func main() {
	run()
	if failed {
		os.Exit(1)
	}
}
