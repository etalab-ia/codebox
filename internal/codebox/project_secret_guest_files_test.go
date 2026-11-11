package codebox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
)

// guestFSClient is the fake sandbox client with a small in-memory guest
// filesystem, so the publish path can be exercised for real: writes land in
// the map, the atomic rename actually moves the temporary file, and reads see
// what was written. The base fake records calls and provides every other
// method unchanged.
type guestFSClient struct {
	*fakeMSBClient
	files map[string]string
	// modes records the permission set for a path, so a test can assert that
	// a file holding a real secret was restricted before it became visible
	// under its final name.
	modes map[string]os.FileMode
	// tmpModeOverride forces the mode of a pre-created temporary file to
	// something else. Without it there is no way to show that the published
	// mode check fires: the fake would always create 0600 and the check would
	// be a line that can never fail.
	tmpModeOverride *os.FileMode
}

func newGuestFSClient() *guestFSClient {
	return &guestFSClient{fakeMSBClient: &fakeMSBClient{}, files: map[string]string{}, modes: map[string]os.FileMode{}}
}

func (c *guestFSClient) WriteFile(_ context.Context, name, guestPath string, data []byte) error {
	c.record("writefile " + name + " " + guestPath)
	c.files[guestPath] = string(data)
	// The mode is deliberately left alone: the real runtime writes into an
	// existing file without changing its permissions (measured by the
	// phase-4 probe as `rewrite-mode`), and a fake that reset the mode here
	// would be stricter than the runtime and hide the very window the
	// pre-creation exists to close.
	return nil
}

// guestFileFromCommand extracts the path a codebox guest command operates
// on. Every command this package issues begins by binding the target to `f`.
func guestFileFromCommand(command string) (string, bool) {
	const marker = "f="
	idx := strings.Index(command, marker)
	if idx < 0 {
		return "", false
	}
	rest := command[idx+len(marker):]
	quote := rest[0]
	if quote != '\'' && quote != '"' {
		return "", false
	}
	end := strings.IndexByte(rest[1:], quote)
	if end < 0 {
		return "", false
	}
	return rest[1 : 1+end], true
}

// Exec understands only the shapes this package issues: mkdir -p, chmod, the
// atomic mv, the header-checked removal, and plain rm -f. Anything else is a
// test bug and must not be reported as success.
func (c *guestFSClient) Exec(_ context.Context, name, command string) (int, string, error) {
	c.record("exec " + name + " " + command)
	switch {
	case strings.HasPrefix(command, "mkdir -p "):
		return 0, "", nil
	case strings.HasPrefix(command, "umask 077; : > "):
		// Pre-create the temporary file with the restrictive mode, before any
		// payload exists. umask applies to the creation, so the file is 0600
		// from the first instant, as it is in the guest.
		path := unquoteGuest(strings.TrimPrefix(command, "umask 077; : > "))
		c.files[path] = ""
		c.modes[path] = 0o600
		if c.tmpModeOverride != nil {
			c.modes[path] = *c.tmpModeOverride
		}
		return 0, "", nil
	case strings.Contains(command, "stat -c %a"):
		// The mode check and the rename are one guest invocation, so a file
		// that lost its restriction is never published under its final name.
		path, ok := guestFileFromCommand(command)
		if !ok {
			return 1, "unexpected move shape", nil
		}
		if c.modes[path] != 0o600 {
			delete(c.files, path)
			delete(c.modes, path)
			return 5, "mode is not 600", nil
		}
		idx := strings.LastIndex(command, "mv -f ")
		if idx < 0 {
			return 1, "unexpected move shape", nil
		}
		parts := strings.Fields(command[idx:])
		if len(parts) != 4 {
			return 1, "unexpected mv shape", nil
		}
		src, dst := unquoteGuest(parts[2]), unquoteGuest(parts[3])
		content, ok := c.files[src]
		if !ok {
			return 1, "no such file", nil
		}
		delete(c.files, src)
		c.files[dst] = content
		delete(c.modes, src)
		c.modes[dst] = 0o600
		return 0, "", nil
	case strings.HasPrefix(command, "chmod "):
		// chmod <mode> <path>: the mode is recorded per path, so a test can
		// assert the restriction happened on the temporary file, before the
		// content was reachable under its final name.
		parts := strings.Fields(command)
		if len(parts) != 3 {
			return 1, "unexpected chmod shape", nil
		}
		mode, err := strconv.ParseUint(parts[1], 8, 32)
		if err != nil {
			return 1, "unexpected chmod mode", nil
		}
		c.modes[unquoteGuest(parts[2])] = os.FileMode(mode)
		return 0, "", nil
	case strings.HasPrefix(command, "mv -f "):
		parts := strings.Fields(command)
		if len(parts) != 4 {
			return 1, "unexpected mv shape", nil
		}
		src, dst := unquoteGuest(parts[2]), unquoteGuest(parts[3])
		content, ok := c.files[src]
		if !ok {
			return 1, "no such file", nil
		}
		delete(c.files, src)
		c.files[dst] = content
		if mode, ok := c.modes[src]; ok {
			delete(c.modes, src)
			c.modes[dst] = mode
		}
		return 0, "", nil
	case strings.Contains(command, "grep -qxF"):
		// Header-checked removal: only a file still bearing codebox's
		// header is deleted, mirroring the guest script.
		path, ok := guestFileFromCommand(command)
		if !ok {
			return 1, "unexpected removal shape", nil
		}
		content, present := c.files[path]
		if !present {
			return 0, "", nil
		}
		first := content
		if idx := strings.IndexByte(content, '\n'); idx >= 0 {
			first = content[:idx]
		}
		if first == guestSecretHeaderPrefix {
			delete(c.files, path)
		}
		return 0, "", nil
	case strings.HasPrefix(command, "rm -f "):
		delete(c.files, unquoteGuest(strings.TrimPrefix(command, "rm -f ")))
		return 0, "", nil
	default:
		return 1, "unsupported command", nil
	}
}

func (c *guestFSClient) ExecCapture(_ context.Context, name, command string) (string, string, int, error) {
	c.record("execcapture " + name + " " + command)
	path, ok := guestFileFromCommand(command)
	if !ok {
		return "", "unsupported command", 1, nil
	}
	content, present := c.files[path]
	if !present {
		return "", "", 3, nil
	}
	return content, "", 0, nil
}

// ExecCaptureStdin answers the observation command the way the real guest
// does: the expected digest arrives on stdin, the file's first line is
// compared byte for byte, and only a digest match prints the token. The fake
// implements the contract the shell script relies on — `read -r` for the
// digest, and a literal string comparison for the header — because a fake that
// echoed the file would hide exactly the leak the real command is built to
// avoid, and a fake that normalised the header (stripping CR or NUL, as a shell
// variable would) would hide the forgeries the real command must reject.
func (c *guestFSClient) ExecCaptureStdin(_ context.Context, name, command string, stdin []byte) (string, string, int, error) {
	c.record("execcapturestdin " + name + " " + command)
	if !strings.Contains(command, "sha256sum") {
		return "", "unsupported command", 1, nil
	}
	path, ok := guestFileFromCommand(command)
	if !ok {
		return "", "unsupported command", 1, nil
	}
	content, present := c.files[path]
	if !present {
		return "", "", 3, nil
	}
	want := strings.TrimRight(string(stdin), "\n")
	// Byte-exact, over the first guestSecretHeaderLineBytes bytes: the same
	// comparison the guest performs, so a header that differs by an embedded
	// NUL or CR — or by anything following the marker on the same line — is
	// not ours. A fake that trimmed or prefix-matched would accept forgeries
	// the real command rejects.
	line := content
	if len(line) > guestSecretHeaderLineBytes {
		line = line[:guestSecretHeaderLineBytes]
	}
	out := "managed=no\n"
	if len(line) == guestSecretHeaderLineBytes {
		sum := sha256.Sum256([]byte(line))
		if hex.EncodeToString(sum[:]) == guestSecretHeaderLineDigest {
			out = "managed=yes\n"
		}
	}
	sum := sha256.Sum256([]byte(content))
	if hex.EncodeToString(sum[:]) == want {
		out += guestSecretDigestToken + "\n"
	}
	return out, "", 0, nil
}

func unquoteGuest(value string) string {
	return strings.Trim(value, "'\"")
}

func newGuestFSRuntime(t *testing.T, files map[string]string) (*MicrosandboxRuntime, *guestFSClient) {
	t.Helper()
	client := newGuestFSClient()
	for path, content := range files {
		client.files[path] = content
	}
	// newTestMicrosandbox wires the shared defaults (state dir, probe, the
	// "guest already provisioned" exec default); the client is replaced
	// afterwards with this wrapper, which keeps the file semantics.
	m := newTestMicrosandbox(t, client.fakeMSBClient)
	m.Client = client
	return m, client
}

func testDelivery(component, name, guestEnv, marker string, hosts ...string) ProjectSecretDelivery {
	return ProjectSecretDelivery{
		ProjectSecretBinding: ProjectSecretBinding{
			Profile: "development", Component: component, Name: name,
			GuestEnv: guestEnv, Destinations: hosts,
		},
		Reference: "project-env-ns-" + name,
		Marker:    marker,
	}
}

func TestBuildGuestSecretFilesRendersMarkersOnly(t *testing.T) {
	files, err := BuildGuestSecretFiles([]ProjectSecretDelivery{
		testDelivery("apps/api/.env", "API_KEY", "JC_APPS_API_API_KEY", "__CODEBOX_SECRET_aaa__", "api.example.com"),
		testDelivery(".env", "API_KEY", "JC_API_KEY", "__CODEBOX_SECRET_bbb__", "api.example.com"),
		testDelivery("apps/api/.env", "DB_URL", "JC_APPS_API_DB_URL", "__CODEBOX_SECRET_ccc__", "db.example.com"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Component != ".env" || files[1].Component != "apps/api/.env" {
		t.Fatalf("one file per component, sorted: %+v", files)
	}
	for _, f := range files {
		// The header is the contract the guest-side inspect check mirrors
		// (see observeGuestSecretFile), so the rendered first line must be
		// exactly the constant, with no trailing carriage return.
		firstLine := f.Content
		if idx := strings.IndexByte(f.Content, '\n'); idx >= 0 {
			firstLine = f.Content[:idx]
		}
		if firstLine != guestSecretHeaderPrefix {
			t.Fatalf("%s must carry the managed header: %q", f.Component, f.Content)
		}
		if strings.Contains(f.Content, "project-env-") {
			t.Fatalf("%s must not leak the credential reference", f.Component)
		}
	}
	api := files[1].Content
	if !strings.Contains(api, "API_KEY=__CODEBOX_SECRET_aaa__\n") || !strings.Contains(api, "DB_URL=__CODEBOX_SECRET_ccc__\n") {
		t.Fatalf("api component content: %q", api)
	}
	root := files[0].Content
	if !strings.Contains(root, "API_KEY=__CODEBOX_SECRET_bbb__\n") {
		t.Fatalf("root component content: %q", root)
	}
	// Two components may share a variable name with different markers.
	if strings.Contains(root, "__CODEBOX_SECRET_aaa__") || strings.Contains(api, "__CODEBOX_SECRET_bbb__") {
		t.Fatal("component values must not bleed into each other")
	}
	// Variables are ordered so the file is stable across runs.
	if strings.Index(api, "API_KEY=") > strings.Index(api, "DB_URL=") {
		t.Fatal("variables must be rendered in sorted order")
	}
}

func TestBuildGuestSecretFilesRejectsUnsafeComponents(t *testing.T) {
	for _, component := range []string{"../.env", "/tmp/.env", "apps/../../.env", "apps\\api\\.env", "notes.txt", ""} {
		if _, err := BuildGuestSecretFiles([]ProjectSecretDelivery{
			testDelivery(component, "API_KEY", "JC_API_KEY", "__CODEBOX_SECRET_aaa__", "api.example.com"),
		}); err == nil {
			t.Fatalf("component %q must be refused", component)
		}
	}
	if _, err := BuildGuestSecretFiles([]ProjectSecretDelivery{
		testDelivery(".env", "API_KEY", "JC_API_KEY", "", "api.example.com"),
	}); err == nil {
		t.Fatal("a delivery with no marker must be refused rather than rendered empty")
	}
	if _, err := BuildGuestSecretFiles([]ProjectSecretDelivery{
		testDelivery(".env", "API_KEY", "JC_API_KEY", "m", "api.example.com"),
		testDelivery(".env", "API_KEY", "JC_OTHER", "m2", "api.example.com"),
	}); err == nil {
		t.Fatal("two bindings for one variable in one component must be refused")
	}
}

func TestGuestWorkspaceDotenvPathStaysInsideWorkspace(t *testing.T) {
	got, err := guestWorkspaceDotenvPath("apps/api/.env")
	if err != nil || got != msbGuestWorkspace+"/apps/api/.env" {
		t.Fatalf("resolved path: %q, %v", got, err)
	}
	for _, component := range []string{"../escape.env", "/etc/passwd", "a/../../b.env"} {
		if _, err := guestWorkspaceDotenvPath(component); err == nil {
			t.Fatalf("component %q must not resolve", component)
		}
	}
}

func TestPlanGuestSecretPublish(t *testing.T) {
	desired, err := BuildGuestSecretFiles([]ProjectSecretDelivery{
		testDelivery(".env", "API_KEY", "JC_API_KEY", "__CODEBOX_SECRET_aaa__", "api.example.com"),
	})
	if err != nil {
		t.Fatal(err)
	}
	apiPath := msbGuestWorkspace + "/.env"
	otherPath := msbGuestWorkspace + "/old/.env"
	// The observations below carry no content, by construction: the plan
	// decides on "is it ours" and "is it unchanged", never on the bytes.
	ourUnchanged := GuestSecretPublishObservation{Present: true, Managed: true, Match: true}
	ourChanged := GuestSecretPublishObservation{Present: true, Managed: true}
	notOurs := GuestSecretPublishObservation{Present: true}

	// Nothing in the guest yet: a fresh write.
	plan, err := PlanGuestSecretPublish(desired, map[string]GuestSecretPublishObservation{}, nil)
	if err != nil || len(plan.Writes) != 1 || len(plan.Conflicts) != 0 || len(plan.Removes) != 0 {
		t.Fatalf("fresh publish: %+v, %v", plan, err)
	}

	// Identical managed content: no write, so a restart is a true no-op.
	plan, err = PlanGuestSecretPublish(desired, map[string]GuestSecretPublishObservation{apiPath: ourUnchanged}, []string{apiPath})
	if err != nil || len(plan.Writes) != 0 || len(plan.Unchanged) != 1 {
		t.Fatalf("idempotent publish: %+v, %v", plan, err)
	}

	// Changed managed content: rewrite.
	plan, err = PlanGuestSecretPublish(desired, map[string]GuestSecretPublishObservation{apiPath: ourChanged}, []string{apiPath})
	if err != nil || len(plan.Writes) != 1 {
		t.Fatalf("managed update: %+v, %v", plan, err)
	}

	// A file the project ships is never overwritten.
	plan, err = PlanGuestSecretPublish(desired, map[string]GuestSecretPublishObservation{apiPath: notOurs}, nil)
	if err != nil || len(plan.Writes) != 0 || len(plan.Conflicts) != 1 {
		t.Fatalf("conflict must be reported, not resolved: %+v, %v", plan, err)
	}
	// And the refusal is enforced at apply time too.
	m, _ := newGuestFSRuntime(t, map[string]string{apiPath: "API_KEY=the-projects-own-value\n"})
	if err := m.publishGuestSecretFiles(t.Context(), plan); err == nil {
		t.Fatal("applying a plan with a conflict must be refused")
	}

	// A previously managed file that is no longer wanted is removed.
	plan, err = PlanGuestSecretPublish(desired, map[string]GuestSecretPublishObservation{apiPath: ourUnchanged, otherPath: ourChanged}, []string{apiPath, otherPath})
	if err != nil || len(plan.Removes) != 1 || plan.Removes[0] != otherPath {
		t.Fatalf("obsolete managed file: %+v, %v", plan, err)
	}

	// A file the user replaced with their own is theirs: never removed.
	plan, err = PlanGuestSecretPublish(desired, map[string]GuestSecretPublishObservation{otherPath: notOurs}, []string{otherPath})
	if err != nil || len(plan.Removes) != 0 || len(plan.Writes) != 1 {
		t.Fatalf("a user-replaced file must survive: %+v, %v", plan, err)
	}

	// No desired files at all: everything previously managed goes away.
	plan, err = PlanGuestSecretPublish(nil, map[string]GuestSecretPublishObservation{apiPath: ourUnchanged}, []string{apiPath})
	if err != nil || len(plan.Removes) != 1 || len(plan.Writes) != 0 {
		t.Fatalf("revoking every approval must clean up: %+v, %v", plan, err)
	}
}

// TestInspectingAManagedFileNeverReadsItBack is the regression for the leak
// issue #190 names: the runtime persists the stdout of guest executions in a
// host log, so a command that prints a managed file copies its plaintext there.
// This asserts on the command actually issued, not on its result, because a
// `cat` that happens to return the right bytes is exactly the leak.
func TestInspectingAManagedFileNeverReadsItBack(t *testing.T) {
	const secret = "local-secret-value-that-must-not-be-printed"
	guestPath := msbGuestWorkspace + "/.env"
	deliveries := []ProjectSecretDelivery{guestDelivery(".env", "DB_PASSWORD", secret)}
	content, err := BuildGuestSecretFiles(deliveries)
	if err != nil {
		t.Fatal(err)
	}
	wantedDigestHex := digestOfGuestSecretContent(content[0].Content)

	first, firstClient := newGuestFSRuntime(t, nil)
	if _, err := first.syncGuestSecretFiles(t.Context(), deliveries, nil); err != nil {
		t.Fatal(err)
	}

	// The second pass inspects an existing file, which is the path that used
	// to read it back with cat.
	second, client := newGuestFSRuntime(t, firstClient.files)
	// The assertions below run whether or not the sync succeeds, and before
	// any check on its error: a command that reads the file back is the leak
	// even when the sync then refuses to publish.
	_, syncErr := second.syncGuestSecretFiles(t.Context(), deliveries, []string{guestPath})
	inspected := false
	for _, call := range client.calls {
		if !strings.HasPrefix(call, "execcapture ") && !strings.HasPrefix(call, "execcapturestdin ") {
			continue
		}
		inspected = true
		if strings.Contains(call, "cat ") || strings.Contains(call, `cat"`) || strings.Contains(call, "cat $") {
			t.Fatalf("inspecting a managed file must not read it back through stdout: %v", call)
		}
		if !strings.Contains(call, "sha256sum") {
			t.Fatalf("the inspect command must compare a digest computed in the guest: %v", call)
		}
		if strings.Contains(call, secret) {
			t.Fatalf("the delivered value must never reach the command line: %v", call)
		}
		// The expected digest is derived from the value, so it must not reach
		// argv either: the runtime records exec argv in the same log as
		// stdout, and a digest of a low-entropy value narrows a guess.
		if strings.Contains(call, wantedDigestHex) {
			t.Fatalf("the content digest must travel on stdin, not in the command: %v", call)
		}
	}
	if !inspected {
		t.Fatalf("the second pass did not inspect the existing file: %v", client.calls)
	}
	if syncErr != nil {
		t.Fatalf("inspecting an unchanged managed file must not fail the sync: %v", syncErr)
	}
}

// TestARotatedValueReachesTheGuest is the regression for the bug Codex caught:
// the inspection must compare the file against the content this run wants, not
// against what was written before, or a rotated secret silently stays old.
func TestARotatedValueReachesTheGuest(t *testing.T) {
	guestPath := msbGuestWorkspace + "/.env"
	m, client := newGuestFSRuntime(t, nil)
	if _, err := m.syncGuestSecretFiles(t.Context(), []ProjectSecretDelivery{
		guestDelivery(".env", "DB_PASSWORD", "first-value"),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.files[guestPath], "first-value") {
		t.Fatalf("the first value must be delivered: %q", client.files[guestPath])
	}

	// Rotate the approved value and start again, using the same runtime so the
	// guest file is the one left by the previous pass.
	before := len(client.calls)
	if _, err := m.syncGuestSecretFiles(t.Context(), []ProjectSecretDelivery{
		guestDelivery(".env", "DB_PASSWORD", "rotated-value"),
	}, []string{guestPath}); err != nil {
		t.Fatal(err)
	}
	wrote := false
	for _, call := range client.calls[before:] {
		if strings.Contains(call, "mv -f") {
			wrote = true
		}
	}
	if !wrote {
		t.Fatal("a rotated value must be rewritten: comparing against the previous digest would leave the old secret in the guest")
	}
	if !strings.Contains(client.files[guestPath], "rotated-value") || strings.Contains(client.files[guestPath], "first-value") {
		t.Fatalf("the guest must hold the rotated value and not the old one: %q", client.files[guestPath])
	}
}

func TestSyncGuestSecretFilesWritesOnceAndCleansUp(t *testing.T) {
	m, client := newGuestFSRuntime(t, nil)
	deliveries := []ProjectSecretDelivery{
		testDelivery(".env", "API_KEY", "JC_API_KEY", "__CODEBOX_SECRET_aaa__", "api.example.com"),
	}
	managed, err := m.syncGuestSecretFiles(t.Context(), deliveries, nil)
	if err != nil {
		t.Fatal(err)
	}
	guestPath := msbGuestWorkspace + "/.env"
	if len(managed) != 1 || managed[0] != guestPath {
		t.Fatalf("managed paths: %v", managed)
	}
	written, ok := client.files[guestPath]
	if !ok {
		t.Fatalf("the file was not written: %v", client.files)
	}
	if !strings.Contains(written, "__CODEBOX_SECRET_aaa__") || strings.Contains(written, "project-env-") {
		t.Fatalf("written content: %q", written)
	}
	// The temporary file must not survive the atomic rename.
	for path := range client.files {
		if strings.HasSuffix(path, ".cb-tmp") {
			t.Fatalf("temporary file left behind: %s", path)
		}
	}

	// A second pass with identical content is a no-op.
	before := len(client.calls)
	if _, err := m.syncGuestSecretFiles(t.Context(), deliveries, managed); err != nil {
		t.Fatal(err)
	}
	for _, call := range client.calls[before:] {
		if strings.Contains(call, "mv -f") {
			t.Fatalf("an unchanged file was rewritten: %v", client.calls[before:])
		}
	}

	// Withdrawing every approval removes the managed file.
	if _, err := m.syncGuestSecretFiles(t.Context(), nil, managed); err != nil {
		t.Fatal(err)
	}
	if _, ok := client.files[guestPath]; ok {
		t.Fatal("a withdrawn approval must remove the generated file")
	}
}

// TestGuestSecretHeaderDigestMatchesTheConstant pins the two together. The
// ownership check compares against a digest, so editing the header without
// updating the digest would make every managed file read as foreign and a
// reconciliation would rewrite them; the init assertion turns that into a
// startup panic, and this test says which constant to change.
func TestGuestSecretHeaderDigestMatchesTheConstant(t *testing.T) {
	line := []byte(guestSecretHeaderPrefix + "\n")
	if len(line) != guestSecretHeaderLineBytes {
		t.Fatalf("the header line is %d bytes, not %d: update guestSecretHeaderLineBytes", len(line), guestSecretHeaderLineBytes)
	}
	sum := sha256.Sum256(line)
	if got := hex.EncodeToString(sum[:]); got != guestSecretHeaderLineDigest {
		t.Fatalf("the header digest is %s, not the pinned %s: update guestSecretHeaderLineDigest", got, guestSecretHeaderLineDigest)
	}
}

// TestAForgeableHeaderIsNotTreatedAsOurs is the regression for the class of
// bug three reviews found in the ownership check: anything that makes the
// check normalise or prefix-match the first line turns a foreign file into
// codebox's own, and the plan then overwrites it. An embedded carriage
// return survives a CR strip; an embedded NUL is dropped by shell command
// substitution; and a marker followed by a NUL is matched by grep's
// whole-line mode, because both GNU and the guest's busybox treat such a line
// as binary and match on the readable prefix (busybox does it even with `-a`,
// so that flag does not close it). The check compares a digest over an exact
// byte count, which none of these can satisfy.
func TestAForgeableHeaderIsNotTreatedAsOurs(t *testing.T) {
	guestPath := msbGuestWorkspace + "/.env"
	for name, firstLine := range map[string]string{
		"embedded cref":     "# codebox mana\rged: do not edit",
		"embedded nul":      "# codebox mana\x00ged: do not edit",
		"marker then nul":   guestSecretHeaderPrefix + "\x00evil",
		"marker then bytes": guestSecretHeaderPrefix + "extra",
		"marker then tab":   guestSecretHeaderPrefix + "\t",
		"trailing space":    guestSecretHeaderPrefix + " ",
		"leading space":     " " + guestSecretHeaderPrefix,
		"prefix only":       guestSecretHeaderPrefix[:10],
	} {
		content := firstLine + "\nAPI_KEY=the-projects-own-value\n"
		m, client := newGuestFSRuntime(t, map[string]string{guestPath: content})
		_, err := m.syncGuestSecretFiles(t.Context(), []ProjectSecretDelivery{
			testDelivery(".env", "API_KEY", "JC_API_KEY", "__CODEBOX_SECRET_aaa__", "api.example.com"),
		}, nil)
		if err == nil || !strings.Contains(err.Error(), "were not written by codebox") {
			t.Fatalf("%s: the publish must be refused, not treated as ours: %v", name, err)
		}
		if client.files[guestPath] != content {
			t.Fatalf("%s: the file must be left untouched: %q", name, client.files[guestPath])
		}
	}

	// The control: the exact header IS ours, so the same path is a no-op
	// rather than a conflict, which proves the refusals above are about the
	// forged bytes and not about a check that never matches.
	content := guestSecretHeaderPrefix + "\nAPI_KEY=__CODEBOX_SECRET_aaa__\n"
	m, _ := newGuestFSRuntime(t, map[string]string{guestPath: content})
	if _, err := m.syncGuestSecretFiles(t.Context(), []ProjectSecretDelivery{
		testDelivery(".env", "API_KEY", "JC_API_KEY", "__CODEBOX_SECRET_aaa__", "api.example.com"),
	}, []string{guestPath}); err != nil {
		t.Fatalf("an exact managed header must be recognised: %v", err)
	}
}

func TestSyncGuestSecretFilesRefusesToClobberProjectFile(t *testing.T) {
	guestPath := msbGuestWorkspace + "/.env"
	own := "API_KEY=the-projects-own-value\n"
	m, client := newGuestFSRuntime(t, map[string]string{guestPath: own})
	_, err := m.syncGuestSecretFiles(t.Context(), []ProjectSecretDelivery{
		testDelivery(".env", "API_KEY", "JC_API_KEY", "__CODEBOX_SECRET_aaa__", "api.example.com"),
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "were not written by codebox") {
		t.Fatalf("the publish must be refused: %v", err)
	}
	if client.files[guestPath] != own {
		t.Fatal("the project's own file was modified")
	}
}

func TestSyncGuestSecretFilesWritesEachComponentSeparately(t *testing.T) {
	m, client := newGuestFSRuntime(t, nil)
	managed, err := m.syncGuestSecretFiles(t.Context(), []ProjectSecretDelivery{
		testDelivery(".env", "API_KEY", "JC_API_KEY", "__CODEBOX_SECRET_bbb__", "api.example.com"),
		testDelivery("apps/api/.env", "API_KEY", "JC_APPS_API_API_KEY", "__CODEBOX_SECRET_aaa__", "api.example.com"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 2 {
		t.Fatalf("two components must produce two managed paths: %v", managed)
	}
	// Same variable name, different components, different markers: the
	// framework's own load order is preserved because the files are separate.
	root := client.files[msbGuestWorkspace+"/.env"]
	nested := client.files[msbGuestWorkspace+"/apps/api/.env"]
	if !strings.Contains(root, "__CODEBOX_SECRET_bbb__") || !strings.Contains(nested, "__CODEBOX_SECRET_aaa__") {
		t.Fatalf("components were mixed up: root=%q nested=%q", root, nested)
	}
}

func TestGeneratedMarkerMatchesTheEnvironmentBinding(t *testing.T) {
	// The environment variable and the generated dotenv must expose the same
	// placeholder, or an application reading either source would send a value
	// the proxy does not recognize.
	store := newProjectSecretFakeStore()
	root, state := t.TempDir(), t.TempDir()
	s := projectSecretFixture(t, root, state, store)
	importProjectSecrets(t, s, "development", ".env", map[string]string{"API_KEY": "project-value"})
	if err := SetProjectSecretActiveProfile(DefaultFS, state, s.Namespace(), "development"); err != nil {
		t.Fatal(err)
	}
	if err := ApproveProjectSecretBinding(DefaultFS, state, s.Namespace(), ProjectSecretBinding{
		Profile: "development", Component: ".env", Name: "API_KEY",
		GuestEnv: "JC_API_KEY", Destinations: []string{"api.example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	deliveries, _, err := s.ApprovedDeliveries()
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("deliveries: %+v, %v", deliveries, err)
	}
	if deliveries[0].Marker != ProjectSecretPlaceholder(s.Namespace(), "development", ".env", "API_KEY") {
		t.Fatalf("marker: %q", deliveries[0].Marker)
	}
	files, err := BuildGuestSecretFiles(deliveries)
	if err != nil || len(files) != 1 {
		t.Fatalf("files: %+v, %v", files, err)
	}
	if !strings.Contains(files[0].Content, "API_KEY="+deliveries[0].Marker+"\n") {
		t.Fatalf("the generated file must carry the binding's marker: %q", files[0].Content)
	}
}

var _ msbClient = (*guestFSClient)(nil)
