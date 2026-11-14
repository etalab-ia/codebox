package jcvendor

// Unit-level qualification of the vendored Phase 3 logic. These tests call
// the REAL functions copied from just-code at the pinned commit
// (BuildGuestSecretFiles, GuestSecretFile.GuestPath, PlanGuestSecretPublish,
// isManagedGuestSecretFile, publishGuestSecretFiles, syncGuestSecretFiles,
// ProjectSecretPlaceholder, ProjectSecretGuestEnv) against an in-memory
// guest filesystem. The fake below is kit-authored test scaffolding in the
// same spirit as just-code's own guestFSClient test double; every function
// under test is the vendored one.

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeGuestClient is an in-memory guest: writes land in files, the atomic mv
// actually moves the temporary file, rm removes, cat reads. It understands
// only the command shapes the vendored publish path issues; anything else is
// a test bug, not a silent success.
type fakeGuestClient struct {
	files map[string]string
	calls []string
}

func newFakeGuestClient() *fakeGuestClient {
	return &fakeGuestClient{files: map[string]string{}}
}

func (c *fakeGuestClient) WriteFile(_ context.Context, name, guestPath string, data []byte) error {
	c.calls = append(c.calls, "writefile "+name+" "+guestPath)
	c.files[guestPath] = string(data)
	return nil
}

// guestFileFromCommand extracts the target path: every command the vendored
// publish path issues binds it to `f` first.
func guestFileFromCommand(command string) (string, bool) {
	const marker = "f="
	idx := strings.Index(command, marker)
	if idx < 0 {
		return "", false
	}
	rest := command[idx+len(marker):]
	if rest == "" {
		return "", false
	}
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

func (c *fakeGuestClient) Exec(_ context.Context, name, command string) (int, string, error) {
	c.calls = append(c.calls, "exec "+name+" "+command)
	switch {
	case strings.HasPrefix(command, "mkdir -p "):
		return 0, "", nil
	case strings.HasPrefix(command, "mv -f "):
		parts := strings.Fields(command)
		if len(parts) != 4 {
			return 1, "unexpected mv shape", nil
		}
		src, dst := unquoteFakeGuest(parts[2]), unquoteFakeGuest(parts[3])
		content, ok := c.files[src]
		if !ok {
			return 1, "no such file", nil
		}
		delete(c.files, src)
		c.files[dst] = content
		return 0, "", nil
	case strings.Contains(command, "grep -qxF"):
		// Header-checked removal: only a file still bearing the header goes.
		guestPath, ok := guestFileFromCommand(command)
		if !ok {
			return 1, "unexpected removal shape", nil
		}
		content, present := c.files[guestPath]
		if !present {
			return 0, "", nil
		}
		first := content
		if idx := strings.IndexByte(content, '\n'); idx >= 0 {
			first = content[:idx]
		}
		if first == guestSecretHeaderPrefix {
			delete(c.files, guestPath)
		}
		return 0, "", nil
	case strings.HasPrefix(command, "rm -f "):
		delete(c.files, unquoteFakeGuest(strings.TrimPrefix(command, "rm -f ")))
		return 0, "", nil
	default:
		return 1, "unsupported command", nil
	}
}

func (c *fakeGuestClient) ExecCapture(_ context.Context, name, command string) (string, string, int, error) {
	c.calls = append(c.calls, "execcapture "+name+" "+command)
	guestPath, ok := guestFileFromCommand(command)
	if !ok {
		return "", "unsupported command", 1, nil
	}
	content, present := c.files[guestPath]
	if !present {
		// Exit 3 is the vendored path's "absent" signal; anything else is an
		// unreadable file and must not be reported as absence.
		return "", "", 3, nil
	}
	return content, "", 0, nil
}

func unquoteFakeGuest(value string) string {
	return strings.Trim(value, "'\"")
}

func newFakeRuntime(files map[string]string) (*MicrosandboxRuntime, *fakeGuestClient) {
	client := newFakeGuestClient()
	for guestPath, content := range files {
		client.files[guestPath] = content
	}
	return NewGuestRuntimeForTest(client, "jc-p3-fake"), client
}

// kitDelivery builds the delivery exactly the way the vendored Deliveries
// would: the marker is the vendored ProjectSecretPlaceholder for the
// identity, the guest variable is the vendored ProjectSecretGuestEnv.
func kitDelivery(namespace, component, name string, hosts ...string) ProjectSecretDelivery {
	return ProjectSecretDelivery{
		ProjectSecretBinding: ProjectSecretBinding{
			Profile: "development", Component: component, Name: name,
			GuestEnv: ProjectSecretGuestEnv(component, name), Destinations: hosts,
		},
		Reference: "project-env-" + namespace + "-ref",
		Marker:    ProjectSecretPlaceholder(namespace, "development", component, name),
	}
}

// The Phase 3 headline case: the SAME variable name in TWO components.
func kitDeliveries() []ProjectSecretDelivery {
	return []ProjectSecretDelivery{
		kitDelivery("jc-p3-qual", ".env", "API_KEY", "p3lab.test"),
		kitDelivery("jc-p3-qual", "apps/api/.env", "API_KEY", "p3lab.test"),
	}
}

func TestPlaceholderDerivationIsDistinctAndDollarFree(t *testing.T) {
	root := ProjectSecretPlaceholder("jc-p3-qual", "development", ".env", "API_KEY")
	api := ProjectSecretPlaceholder("jc-p3-qual", "development", "apps/api/.env", "API_KEY")
	if root == api {
		t.Fatal("same variable name in two components must still derive distinct markers")
	}
	for _, m := range []string{root, api} {
		if strings.Contains(m, "$") {
			t.Fatalf("marker %q contains a dollar: interpolation in guest loaders would erase it", m)
		}
		if !strings.HasPrefix(m, "__JUST_CODE_SECRET_") || !strings.HasSuffix(m, "__") {
			t.Fatalf("marker %q lost the opaque placeholder shape", m)
		}
	}
	// Deterministic: a restart presents the same marker.
	if root != ProjectSecretPlaceholder("jc-p3-qual", "development", ".env", "API_KEY") {
		t.Fatal("marker derivation must be deterministic")
	}
	if ProjectSecretGuestEnv(".env", "API_KEY") != "JC_API_KEY" {
		t.Fatalf("root guest env: %q", ProjectSecretGuestEnv(".env", "API_KEY"))
	}
	if ProjectSecretGuestEnv("apps/api/.env", "API_KEY") != "JC_APPS_API_API_KEY" {
		t.Fatalf("nested guest env: %q", ProjectSecretGuestEnv("apps/api/.env", "API_KEY"))
	}
}

func TestBuildGuestSecretFilesSameVariableTwoComponents(t *testing.T) {
	deliveries := kitDeliveries()
	files, err := BuildGuestSecretFiles(deliveries)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Component != ".env" || files[1].Component != "apps/api/.env" {
		t.Fatalf("one file per component, sorted: %+v", files)
	}
	rootMarker := deliveries[0].Marker
	apiMarker := deliveries[1].Marker
	for _, f := range files {
		if !isManagedGuestSecretFile(f.Content) {
			t.Fatalf("%s must carry the managed header: %q", f.Component, f.Content)
		}
		if strings.Contains(f.Content, "project-env-") {
			t.Fatalf("%s must not leak the credential-store reference", f.Component)
		}
		if !strings.Contains(f.Content, "# source: "+f.Component+"\n") {
			t.Fatalf("%s must name its source component: %q", f.Component, f.Content)
		}
	}
	root, api := files[0].Content, files[1].Content
	if !strings.Contains(root, "API_KEY="+rootMarker+"\n") {
		t.Fatalf("root content: %q", root)
	}
	if !strings.Contains(api, "API_KEY="+apiMarker+"\n") {
		t.Fatalf("api content: %q", api)
	}
	// Same variable name, different components: no bleed either way.
	if strings.Contains(root, apiMarker) || strings.Contains(api, rootMarker) {
		t.Fatal("component markers must not bleed into each other")
	}
	// GuestPath resolves each component under the guest workspace.
	for component, want := range map[string]string{
		".env":          "/workspace/.env",
		"apps/api/.env": "/workspace/apps/api/.env",
	} {
		got, err := (GuestSecretFile{Component: component}).GuestPath()
		if err != nil || got != want {
			t.Fatalf("GuestPath(%q) = %q, %v; want %q", component, got, err, want)
		}
	}
}

func TestPlanRefusesTargetJustCodeDidNotWrite(t *testing.T) {
	desired, err := BuildGuestSecretFiles(kitDeliveries())
	if err != nil {
		t.Fatal(err)
	}
	rootPath := "/workspace/.env"
	// The project ships its own .env at the target: not just-code's header.
	existing := map[string]string{rootPath: "API_KEY=the-projects-own-value\n"}
	plan, err := PlanGuestSecretPublish(desired, existing, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Conflicts) != 1 || plan.Conflicts[0] != rootPath {
		t.Fatalf("the foreign file must be a conflict, not a write: %+v", plan)
	}
	for _, w := range plan.Writes {
		if w.Component == ".env" {
			t.Fatalf("the refused target must not be written: %+v", plan)
		}
	}
	// The refusal is enforced at apply time too, and the file survives.
	m, client := newFakeRuntime(existing)
	if err := m.publishGuestSecretFiles(t.Context(), plan); err == nil {
		t.Fatal("applying a plan with a conflict must be refused")
	} else if !strings.Contains(err.Error(), "were not written by just-code") {
		t.Fatalf("refusal must name the cause: %v", err)
	}
	if client.files[rootPath] != "API_KEY=the-projects-own-value\n" {
		t.Fatal("the project's own file was modified")
	}
}

func TestWithdrawApprovalRemovesManagedLeavesForeign(t *testing.T) {
	desired, err := BuildGuestSecretFiles(kitDeliveries())
	if err != nil {
		t.Fatal(err)
	}
	rootPath, apiPath := "/workspace/.env", "/workspace/apps/api/.env"
	oldPath := "/workspace/old/.env"
	managedOld := guestSecretHeaderPrefix + "\n# source: old/.env\nOLD=__JUST_CODE_SECRET_0__\n"

	// Withdrawing the apps/api approval removes its managed file, keeps .env.
	rootOnly, err := BuildGuestSecretFiles(kitDeliveries()[:1])
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PlanGuestSecretPublish(rootOnly,
		map[string]string{rootPath: desired[0].Content, apiPath: desired[1].Content},
		[]string{rootPath, apiPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Removes) != 1 || plan.Removes[0] != apiPath {
		t.Fatalf("withdrawn approval must schedule its file for removal: %+v", plan)
	}
	if len(plan.Unchanged) != 1 || plan.Unchanged[0] != rootPath {
		t.Fatalf("the kept approval must be a no-op: %+v", plan)
	}

	// A previously managed path the USER replaced with their own content is
	// never scheduled for removal.
	plan, err = PlanGuestSecretPublish(rootOnly,
		map[string]string{rootPath: desired[0].Content, oldPath: "MINE=1\n"},
		[]string{rootPath, oldPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Removes) != 0 {
		t.Fatalf("a user-replaced file must survive: %+v", plan)
	}

	// Apply for real against the fake guest: the managed file is rm'd, and a
	// still-managed file at another previously managed path is rm'd too.
	m, client := newFakeRuntime(map[string]string{
		rootPath: desired[0].Content,
		apiPath:  desired[1].Content,
		oldPath:  managedOld,
	})
	managed, err := m.syncGuestSecretFiles(t.Context(), kitDeliveries()[:1], []string{rootPath, apiPath, oldPath})
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 1 || managed[0] != rootPath {
		t.Fatalf("managed paths after withdrawal: %v", managed)
	}
	if _, ok := client.files[apiPath]; ok {
		t.Fatal("withdrawing the apps/api approval must remove its managed file")
	}
	if _, ok := client.files[oldPath]; ok {
		t.Fatal("an obsolete file still carrying the managed header must be removed")
	}
	// The removal must go through the header-checked command for the exact
	// path: the file's disappearance above shows the effect, this shows the
	// guard that keeps a user-replaced file safe.
	removedViaRM := false
	for _, call := range client.calls {
		if strings.Contains(call, "grep -qxF") && strings.Contains(call, "rm -f \"$f\"") &&
			strings.Contains(call, "f='/workspace/apps/api/.env'") {
			removedViaRM = true
		}
	}
	if !removedViaRM {
		t.Fatalf("removal must go through the header-checked command for the exact managed path: %v", client.calls)
	}
	if client.files[rootPath] != desired[0].Content {
		t.Fatal("the surviving approval's file changed")
	}

	// And the full sync path refuses to remove a user-replaced file even when
	// its approval is gone.
	m2, client2 := newFakeRuntime(map[string]string{oldPath: "MINE=1\n"})
	if _, err := m2.syncGuestSecretFiles(t.Context(), nil, []string{oldPath}); err != nil {
		t.Fatal(err)
	}
	if client2.files[oldPath] != "MINE=1\n" {
		t.Fatal("a non-managed file must survive a full withdrawal")
	}
}

func TestSyncPublishesAtomicallyAndIdempotently(t *testing.T) {
	m, client := newFakeRuntime(nil)
	managed, err := m.syncGuestSecretFiles(t.Context(), kitDeliveries(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(managed) != 2 || managed[0] != "/workspace/.env" || managed[1] != "/workspace/apps/api/.env" {
		t.Fatalf("managed paths: %v", managed)
	}
	desired, err := BuildGuestSecretFiles(kitDeliveries())
	if err != nil {
		t.Fatal(err)
	}
	if client.files["/workspace/.env"] != desired[0].Content || client.files["/workspace/apps/api/.env"] != desired[1].Content {
		t.Fatal("the published content must be exactly what BuildGuestSecretFiles rendered")
	}
	// The write went through the sibling temp file + rename, with no leftover.
	sawTmp := false
	for _, call := range client.calls {
		if strings.Contains(call, ".jc-tmp") {
			sawTmp = true
		}
	}
	if !sawTmp {
		t.Fatalf("the atomic write must stage through a temp file: %v", client.calls)
	}
	for guestPath := range client.files {
		if strings.HasSuffix(guestPath, ".jc-tmp") {
			t.Fatalf("temporary file left behind: %s", guestPath)
		}
	}
	// A second pass with identical content issues no write and no rename.
	before := len(client.calls)
	if _, err := m.syncGuestSecretFiles(t.Context(), kitDeliveries(), managed); err != nil {
		t.Fatal(err)
	}
	for _, call := range client.calls[before:] {
		if strings.Contains(call, "mv -f") || strings.HasPrefix(call, "writefile") {
			t.Fatalf("an unchanged file was rewritten: %v", client.calls[before:])
		}
	}
}

func TestBuildGuestSecretFilesRejectsUnsafeAndIncomplete(t *testing.T) {
	for _, component := range []string{"../.env", "/tmp/.env", "apps/../../.env", "apps\\api\\.env", "notes.txt", "", ".env.example"} {
		if _, err := BuildGuestSecretFiles([]ProjectSecretDelivery{
			kitDelivery("jc-p3-qual", component, "API_KEY", "p3lab.test"),
		}); err == nil {
			t.Fatalf("component %q must be refused", component)
		}
	}
	// A delivery with no marker must be refused rather than rendered empty.
	d := kitDelivery("jc-p3-qual", ".env", "API_KEY", "p3lab.test")
	d.Marker = ""
	if _, err := BuildGuestSecretFiles([]ProjectSecretDelivery{d}); err == nil {
		t.Fatal("a delivery with no marker must be refused")
	}
	// Two bindings for one variable in ONE component must be refused.
	d2 := kitDelivery("jc-p3-qual", ".env", "API_KEY", "p3lab.test")
	d2.GuestEnv = "JC_OTHER"
	if _, err := BuildGuestSecretFiles([]ProjectSecretDelivery{
		kitDelivery("jc-p3-qual", ".env", "API_KEY", "p3lab.test"), d2,
	}); err == nil {
		t.Fatal("duplicate variable in one component must be refused")
	}
}

func TestManagedHeaderDetection(t *testing.T) {
	if !isManagedGuestSecretFile(guestSecretHeaderPrefix + "\nA=1\n") {
		t.Fatal("managed header must be detected")
	}
	if !isManagedGuestSecretFile(guestSecretHeaderPrefix + "\r\nA=1\n") {
		t.Fatal("managed header with CRLF must be detected")
	}
	for _, foreign := range []string{"", "A=1\n", "# just-code managed: do not edit extra\n", " # just-code managed: do not edit\n"} {
		if isManagedGuestSecretFile(foreign) {
			t.Fatalf("foreign content must not be claimed: %q", foreign)
		}
	}
}

// The approvals round-trip exercises the vendored bindings persistence
// (Write/ReadProjectSecretBindings, Approve/RevokeProjectSecretBinding)
// against the REAL host filesystem in a temp dir — the same calls a CLI
// user drives, producing the same binding records the runtime consumes.
func TestApprovalLifecycleRoundTrip(t *testing.T) {
	state := t.TempDir()
	namespace := "jc-p3-qual"
	path := ProjectSecretBindingsPath(state, namespace)
	if err := SetProjectSecretActiveProfile(DefaultFS, state, namespace, "development"); err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{".env", "apps/api/.env"} {
		if err := ApproveProjectSecretBinding(DefaultFS, state, namespace, ProjectSecretBinding{
			Profile: "development", Component: component, Name: "API_KEY",
			Destinations: []string{"p3lab.test"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	set, err := ReadProjectSecretBindings(DefaultFS, path, namespace)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Bindings) != 2 || set.Profile != "development" {
		t.Fatalf("approved set: %+v", set)
	}
	// GuestEnv was derived by the approval call: distinct per component.
	byComponent := map[string]string{}
	for _, b := range set.Bindings {
		byComponent[b.Component] = b.GuestEnv
	}
	if byComponent[".env"] != "JC_API_KEY" || byComponent["apps/api/.env"] != "JC_APPS_API_API_KEY" {
		t.Fatalf("derived guest envs: %v", byComponent)
	}
	// Revoking one approval leaves the other intact.
	removed, err := RevokeProjectSecretBinding(DefaultFS, state, namespace, "development", "apps/api/.env", "API_KEY")
	if err != nil || !removed {
		t.Fatalf("revoke: %v %v", removed, err)
	}
	set, err = ReadProjectSecretBindings(DefaultFS, path, namespace)
	if err != nil || len(set.Bindings) != 1 || set.Bindings[0].Component != ".env" {
		t.Fatalf("set after revoke: %+v %v", set, err)
	}
	// Deliveries built from the remaining approval render one file.
	d := ProjectSecretDelivery{
		ProjectSecretBinding: set.Bindings[0],
		Reference:            "project-env-" + namespace + "-ref",
		Marker:               ProjectSecretPlaceholder(namespace, "development", ".env", "API_KEY"),
	}
	files, err := BuildGuestSecretFiles([]ProjectSecretDelivery{d})
	if err != nil || len(files) != 1 || files[0].Component != ".env" {
		t.Fatalf("files: %+v %v", files, err)
	}
	if !strings.Contains(files[0].Content, "API_KEY="+d.Marker+"\n") {
		t.Fatalf("rendered file must carry the derived marker: %q", files[0].Content)
	}
}

var _ = fmt.Sprintf // keep fmt imported for future diagnostics in tests
