package main

// Phase 3 native probe (TEST ARTIFACT ONLY).
//
// Qualifies the codebox project-secrets Phase 3 end-to-end path against one
// real Microsandbox 0.7.6 VM on native macOS arm64:
//
//  1. the REAL vendored BuildGuestSecretFiles renders two deliveries that
//     share one variable name (API_KEY) across two components (.env and
//     apps/api/.env), each with its own derived dollar-free marker;
//  2. ONE VM boots with those markers and per-binding destination lists,
//     registered as inert sentinels at create and rotated to host env
//     references, exactly as the pinned runtime does;
//  3. the REAL vendored syncGuestSecretFiles publishes both files into the
//     guest atomically (temp file + mv) over the same SDK calls codebox's
//     sdkMSBClient makes;
//  4. REAL guest applications (Node dotenv@18.0.9 + dotenv-expand@13.0.0,
//     Python python-dotenv==1.2.4) read those files and carry the value in an
//     HTTPS header to the lab; the proxy substitutes the real canary only for
//     the approved destination;
//  5. the two components are asserted not to bleed;
//  6. plan-level refusal/withdrawal semantics are unit-tested in jcvendor.
//
// Only synthetic random canaries are used. Nothing touches the tester's
// ~/.microsandbox, an installed msb, the host trust store, host DNS, or host
// interfaces.

import (
	jcvendor "codebox-native-phase3-kit/jcvendor"
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
	msb "github.com/superradcompany/microsandbox/sdk/go"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const source = "f483a8003d7745dfe3cdea7363d5f366af51eebc"
const archiveSHA = "a4f1722fee6460d4e2813e62b60dae4c092021d8120b7aaa9c2f4b5679d97d89"
const kitRev = "p3v2"

// The fixture project: one variable name, API_KEY, defined in two dotenv
// components. The synthetic namespace scopes the derived markers; no real
// project or credential is involved.
const namespace = "cb-p3-qual"
const profile = "development"
const secretName = "API_KEY"
const rootComponent = ".env"
const apiComponent = "apps/api/.env"
const labHost = "p3lab.test"
const evilHost = "evil.test"

// Markers and guest variables come from the REAL vendored derivations — the
// same functions codebox uses — so the environment, the proxy registration
// and the published file can never disagree about the placeholder.
var markerRoot = jcvendor.ProjectSecretPlaceholder(namespace, profile, rootComponent, secretName)
var markerAPI = jcvendor.ProjectSecretPlaceholder(namespace, profile, apiComponent, secretName)
var guestEnvRoot = jcvendor.ProjectSecretGuestEnv(rootComponent, secretName)
var guestEnvAPI = jcvendor.ProjectSecretGuestEnv(apiComponent, secretName)

// The host transport variables mirror resolveProjectSecretBindings:
// "CODEBOX_HOST_PROJECT_" + guest variable, existing only for the run.
var hostEnvRoot = "CODEBOX_HOST_PROJECT_" + guestEnvRoot
var hostEnvAPI = "CODEBOX_HOST_PROJECT_" + guestEnvAPI

// deliveries is the exact resolved-binding set the runtime would produce for
// this fixture: per-binding destination lists, the derived marker, and an
// opaque store reference that must never reach the guest.
func deliveries() []jcvendor.ProjectSecretDelivery {
	return []jcvendor.ProjectSecretDelivery{
		{
			ProjectSecretBinding: jcvendor.ProjectSecretBinding{
				Profile: profile, Component: rootComponent, Name: secretName,
				GuestEnv: guestEnvRoot, Destinations: []string{labHost},
			},
			Reference: "project-env-" + namespace + "-root",
			Marker:    markerRoot,
		},
		{
			ProjectSecretBinding: jcvendor.ProjectSecretBinding{
				Profile: profile, Component: apiComponent, Name: secretName,
				GuestEnv: guestEnvAPI, Destinations: []string{labHost},
			},
			Reference: "project-env-" + namespace + "-api",
			Marker:    markerAPI,
		},
	}
}

// Guest application fixtures, embedded so the probe binary is self-contained.
//
//go:embed guest
var guestFixtures embed.FS

var failed bool
var sensitive []string
var authorizationRE = regexp.MustCompile(`(?i)(?:proxy-authorization|authorization)[^\r\n]*|(?:basic|bearer)\s+[A-Za-z0-9+/=_\-.]+`)

func scrub(message string) string {
	for _, value := range sensitive {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[REDACTED-CANARY]")
		}
	}
	return authorizationRE.ReplaceAllString(message, "[REDACTED-AUTHORIZATION]")
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
		sensitive = append(sensitive, v, base64.StdEncoding.EncodeToString([]byte(v)), base64.RawStdEncoding.EncodeToString([]byte(v)), base64.StdEncoding.EncodeToString([]byte("user:"+v)))
	}
}

func result(name string, ok bool) {
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

type record struct {
	Path    string            `json:"path"`
	Host    string            `json:"host"`
	Headers map[string]string `json:"headers"`
}

func records(dir string) []record {
	b, e := os.ReadFile(filepath.Join(dir, "requests.jsonl"))
	if e != nil {
		return nil
	}
	var out []record
	for _, line := range strings.Split(string(b), "\n") {
		var r record
		if json.Unmarshal([]byte(line), &r) == nil {
			out = append(out, r)
		}
	}
	return out
}
func find(dir, path string) *record {
	for _, r := range records(dir) {
		if r.Path == path {
			return &r
		}
	}
	return nil
}

// sdkGuestClient implements jcvendor.GuestClient over the real SDK exactly
// the way codebox's sdkMSBClient does at the pinned commit: connect to the
// named sandbox, then Shell for commands and FS().Write for payloads.
type sdkGuestClient struct{}

func connect(ctx context.Context, name string) (*msb.Sandbox, error) {
	h, err := msb.GetSandbox(ctx, name)
	if err != nil {
		return nil, err
	}
	return h.Connect(ctx)
}

func (sdkGuestClient) WriteFile(ctx context.Context, name, guestPath string, data []byte) error {
	sb, err := connect(ctx, name)
	if err != nil {
		return err
	}
	defer func() { _ = sb.Close() }()
	return sb.FS().Write(ctx, guestPath, data)
}

func (sdkGuestClient) Exec(ctx context.Context, name, command string) (int, string, error) {
	sb, err := connect(ctx, name)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = sb.Close() }()
	out, err := sb.Shell(ctx, command)
	if err != nil {
		return 0, "", err
	}
	return out.ExitCode(), out.Stderr(), nil
}

func (sdkGuestClient) ExecCapture(ctx context.Context, name, command string) (string, string, int, error) {
	sb, err := connect(ctx, name)
	if err != nil {
		return "", "", 0, err
	}
	defer func() { _ = sb.Close() }()
	out, err := sb.Shell(ctx, command)
	if err != nil {
		return "", "", 0, err
	}
	return out.Stdout(), out.Stderr(), out.ExitCode(), nil
}

func run() {
	dir := flag.String("dir", "", "owned run directory")
	ip := flag.String("ip", "", "host LAN IP supplied by the test runner")
	tlsPort := flag.Int("tls", 19443, "TLS port")
	dnsPort := flag.Int("dns", 15356, "DNS port")
	cleanup := flag.Bool("cleanup", false, "cleanup own VM from owned directory")
	flag.Parse()
	home := os.Getenv("MSB_HOME")
	if *dir == "" || home != filepath.Join(*dir, "msb-home") || os.Getenv("MSB_PATH") != filepath.Join(*dir, "runtime", "msb") {
		result("isolated-runtime-preflight", false)
		return
	}
	nameFile := filepath.Join(*dir, "vm-name")
	if *cleanup {
		b, e := os.ReadFile(nameFile)
		if e != nil {
			return
		}
		name := strings.TrimSpace(string(b))
		if !strings.HasPrefix(name, "cb-p3-") {
			result("cleanup-ownership", false)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		h, e := msb.GetSandbox(ctx, name)
		if e == nil {
			diagnostic("cleanup-kill", h.Kill(ctx), "")
			e = msb.RemoveSandbox(ctx, name)
		}
		if !msb.IsKind(e, msb.ErrSandboxNotFound) {
			diagnostic("cleanup-get-or-remove", e, "")
		}
		result("cleanup", e == nil || msb.IsKind(e, msb.ErrSandboxNotFound))
		return
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		result("native-darwin-arm64-required", false)
		return
	}
	fmt.Printf("META kit=%s source=%s sdk=0.7.6 runtime=0.7.6 archive_sha256=%s go=%s\n", kitRev, source, archiveSHA, runtime.Version())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 14*time.Minute)
	defer cancel()
	name := "cb-p3-" + random()[:16]
	if e := os.WriteFile(nameFile, []byte(name), 0600); e != nil {
		diagnostic("ownership-file", e, "")
		result("ownership-file", false)
		return
	}
	defer func() {
		c, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		h, e := msb.GetSandbox(c, name)
		if e == nil {
			diagnostic("cleanup-kill", h.Kill(c), "")
			e = msb.RemoveSandbox(c, name)
		}
		if !msb.IsKind(e, msb.ErrSandboxNotFound) {
			diagnostic("cleanup-own-get-or-remove", e, "")
		}
		result("cleanup-own-vm", e == nil || msb.IsKind(e, msb.ErrSandboxNotFound))
	}()
	// Two synthetic random canaries, one per component. They are the only
	// values the lab should ever observe; nothing is read from any store.
	rootCanary, apiCanary := random(), random()
	registerCanaries(rootCanary, apiCanary)
	_ = os.Setenv(hostEnvRoot, rootCanary)
	_ = os.Setenv(hostEnvAPI, apiCanary)
	defer os.Unsetenv(hostEnvRoot)
	defer os.Unsetenv(hostEnvAPI)
	no := false
	net := msb.NetworkPolicy.AllowAll()
	net.TrustHostCAs = &no
	net.DNS = &msb.DNSConfig{Nameservers: []string{fmt.Sprintf("%s:%d", *ip, *dnsPort)}, RebindProtection: &no}
	net.TLS = &msb.TLSConfig{InterceptedPorts: []uint16{uint16(*tlsPort)}, CACert: filepath.Join(*dir, "ca.crt"), CAKey: filepath.Join(*dir, "ca.key"), UpstreamCACerts: []string{filepath.Join(*dir, "ca.crt")}}
	// Boot ONE VM the way the pinned runtime does: the SDK create surface
	// only accepts inline values, so both bindings register the inert
	// sentinel with their custom dollar-free marker and per-binding
	// destination list, and the rotation to host env references follows
	// immediately under ModificationPolicyNoRestart.
	s, e := msb.CreateSandbox(ctx, name,
		msb.WithImage("docker.io/library/python:3.12-alpine"),
		msb.WithCPUs(2), msb.WithMemory(768), msb.WithDetached(),
		msb.WithMaxDuration(12*time.Minute),
		msb.WithEntrypoint("/bin/sh", "-c"), msb.WithCmd("sleep 720"),
		msb.WithNetwork(net),
		msb.WithSecrets(
			msb.Secret.Env(guestEnvRoot, "$MSB_BOOTSTRAP_UNSET", msb.SecretEnvOptions{Allow: []string{labHost}, Placeholder: markerRoot}),
			msb.Secret.Env(guestEnvAPI, "$MSB_BOOTSTRAP_UNSET", msb.SecretEnvOptions{Allow: []string{labHost}, Placeholder: markerAPI}),
		))
	diagnostic("create-inert-sentinels", e, "")
	result("create-inert-sentinels", e == nil)
	if e != nil {
		return
	}
	h, e := msb.GetSandbox(ctx, name)
	if e != nil {
		diagnostic("get-handle", e, "")
		result("get-handle", false)
		return
	}
	plan, e := h.Modify(ctx, msb.ModifyOptions{Policy: msb.ModificationPolicyNoRestart, Secrets: map[string]msb.SecretModifySpec{
		guestEnvRoot: {Env: hostEnvRoot, Placeholder: markerRoot, AllowedHosts: []string{labHost}},
		guestEnvAPI:  {Env: hostEnvAPI, Placeholder: markerAPI, AllowedHosts: []string{labHost}},
	}})
	diagnostic("rotate-to-envrefs", e, "")
	if e == nil && (plan == nil || !plan.Applied) {
		detail, _ := json.Marshal(plan)
		diagnostic("rotate-to-envrefs", nil, "unapplied modification plan: "+string(detail))
	}
	rotated := e == nil && plan != nil && plan.Applied
	result("rotate-to-envrefs", rotated)
	if !rotated {
		return
	}

	exec := func(timeout time.Duration, cmd string, args ...string) (*msb.ExecOutput, error) {
		c, d := context.WithTimeout(ctx, timeout+5*time.Second)
		defer d()
		out, err := s.Exec(c, cmd, args, msb.WithExecTimeout(timeout))
		diagnostic("guest-exec", err, "")
		if err == nil && out != nil && !out.Success() {
			diagnostic("guest-exec", nil, fmt.Sprintf("cmd=%s exit_code=%d stderr=%s", cmd, out.ExitCode(), out.Stderr()))
		}
		return out, err
	}

	// The guest environment holds exactly the two derived markers, distinct
	// per component, before any file exists.
	out, e := exec(20*time.Second, "python3", "-c", "import os,json; print(json.dumps([os.environ.get('"+guestEnvRoot+"'),os.environ.get('"+guestEnvAPI+"')]))")
	var env []string
	if e == nil {
		diagnostic("guest-env-json", json.Unmarshal([]byte(out.Stdout()), &env), "")
	}
	result("exact-guest-env-markers", e == nil && len(env) == 2 && env[0] == markerRoot && env[1] == markerAPI && markerRoot != markerAPI)

	// Publish through the REAL vendored pipeline: BuildGuestSecretFiles,
	// readGuestSecretFiles, PlanGuestSecretPublish, publishGuestSecretFiles —
	// with the SDK calls codebox's own client makes.
	rt := jcvendor.NewGuestRuntimeForTest(sdkGuestClient{}, name)
	publishCtx, publishDone := context.WithTimeout(ctx, 60*time.Second)
	managed, e := rt.SyncGuestSecretFilesForTest(publishCtx, deliveries(), nil)
	publishDone()
	diagnostic("sync-guest-secret-files", e, "")
	wantPaths := []string{"/workspace/.env", "/workspace/apps/api/.env"}
	rootPath, rootPathErr := jcvendor.GuestWorkspaceDotenvPathForTest(rootComponent)
	apiPath, apiPathErr := jcvendor.GuestWorkspaceDotenvPathForTest(apiComponent)
	pathsOK := e == nil && rootPathErr == nil && apiPathErr == nil && len(managed) == 2 && managed[0] == rootPath && managed[1] == apiPath && rootPath == wantPaths[0] && apiPath == wantPaths[1]
	result("published-managed-paths", pathsOK)
	if !pathsOK {
		return
	}

	// The files in the guest are byte-identical to what the REAL renderer
	// produced, carry only their own marker, and hold no canary.
	desired, e := jcvendor.BuildGuestSecretFiles(deliveries())
	diagnostic("build-guest-secret-files", e, "")
	if e != nil || len(desired) != 2 {
		result("root-file-exact-marker-only", false)
		result("api-file-exact-marker-only", false)
		return
	}
	cat := func(guestPath string) string {
		stdout, stderr, code, err := rt.Client.ExecCapture(ctx, name, "cat '"+guestPath+"'")
		diagnostic("cat-guest-file", err, fmt.Sprintf("path=%s code=%d stderr=%s", guestPath, code, stderr))
		if err != nil || code != 0 {
			return ""
		}
		return stdout
	}
	rootContent, apiContent := cat(rootPath), cat(apiPath)
	result("root-file-exact-marker-only", rootContent == desired[0].Content &&
		strings.Contains(rootContent, secretName+"="+markerRoot+"\n") &&
		!strings.Contains(rootContent, markerAPI) &&
		!strings.Contains(rootContent, rootCanary) && !strings.Contains(rootContent, apiCanary) &&
		!strings.Contains(rootContent, "project-env-"))
	result("api-file-exact-marker-only", apiContent == desired[1].Content &&
		strings.Contains(apiContent, secretName+"="+markerAPI+"\n") &&
		!strings.Contains(apiContent, markerRoot) &&
		!strings.Contains(apiContent, rootCanary) && !strings.Contains(apiContent, apiCanary) &&
		!strings.Contains(apiContent, "project-env-"))
	out, e = exec(20*time.Second, "ls", "-la", "/workspace", "/workspace/apps/api")
	result("no-temp-file-left", e == nil && out != nil && out.Success() && !strings.Contains(out.Stdout(), ".cb-tmp"))

	// Push the embedded guest fixtures and install the pinned loaders inside
	// the disposable guest. The packages are fetched by the guest itself from
	// public registries through the lab's DNS relay; nothing is installed on
	// the host.
	push := func(guestPath, embedPath string) bool {
		data, err := guestFixtures.ReadFile(embedPath)
		if err != nil {
			diagnostic("push-read-embed", err, embedPath)
			return false
		}
		// The sandbox write API does not create parents: the fixture
		// directory has to exist before the file is written. Without this the
		// first run fails at the first fixture with a bare "no such file".
		if code, stderr, e := rt.Client.Exec(ctx, name, "mkdir -p "+kitQuote(pathDir(guestPath))); e != nil || code != 0 {
			diagnostic("push-mkdir", e, guestPath+" "+stderr)
			return false
		}
		if err := rt.Client.WriteFile(ctx, name, guestPath, data); err != nil {
			diagnostic("push-write", err, guestPath)
			return false
		}
		return true
	}
	pushed := push("/p3app/node/app.mjs", "guest/node-app/app.mjs") &&
		push("/p3app/node/package.json", "guest/node-app/package.json") &&
		push("/p3app/node/package-lock.json", "guest/node-app/package-lock.json") &&
		push("/p3app/python/app.py", "guest/python-app/app.py") &&
		push("/p3app/python/requirements.txt", "guest/python-app/requirements.txt")
	result("guest-fixtures-pushed", pushed)
	if !pushed {
		return
	}
	out, e = exec(300*time.Second, "apk", "add", "--no-cache", "nodejs", "npm")
	nodeInstalled := e == nil && out != nil && out.Success()
	if nodeInstalled {
		out, e = exec(300*time.Second, "npm", "ci", "--prefix", "/p3app/node", "--no-audit", "--no-fund")
		nodeInstalled = e == nil && out != nil && out.Success()
	}
	result("node-runtime-and-loaders-installed", nodeInstalled)
	out, e = exec(240*time.Second, "pip", "install", "--no-cache-dir", "-r", "/p3app/python/requirements.txt")
	pythonInstalled := e == nil && out != nil && out.Success()
	result("python-loader-installed", pythonInstalled)
	if !nodeInstalled || !pythonInstalled {
		return
	}

	// Run the REAL applications against the managed files. Each prints the
	// parsed value (a marker, not a credential) before the request, so the
	// probe proves the file itself was parsed.
	tls := *tlsPort
	runApp := func(cmd string, args ...string) (string, bool) {
		out, err := exec(30*time.Second, cmd, args...)
		if err != nil || out == nil {
			diagnostic("run-app", err, cmd)
			return "", false
		}
		return out.Stdout(), out.Success()
	}
	nodeOut, nodeOK := runApp("node", "/p3app/node/app.mjs", apiPath, fmt.Sprintf("https://%s:%d/node-api", labHost, tls))
	pythonOut, pythonOK := runApp("python3", "/p3app/python/app.py", rootPath, fmt.Sprintf("https://%s:%d/python-root", labHost, tls))
	result("node-parses-file-marker", nodeOK && strings.Contains(nodeOut, "API_KEY="+markerAPI+"\n") && strings.Contains(nodeOut, "HTTP_STATUS=200"))
	result("python-parses-file-marker", pythonOK && strings.Contains(pythonOut, "API_KEY="+markerRoot+"\n") && strings.Contains(pythonOut, "HTTP_STATUS=200"))

	// The lab received the substituted REAL value for BOTH components: the
	// proxy recognized each file's marker and swapped in that component's
	// canary, toward the approved destination only.
	r := find(*dir, "/node-api")
	result("node-header-substituted", r != nil && r.Host == labHost && r.Headers["x-api-key"] == digest(apiCanary))
	r = find(*dir, "/python-root")
	result("python-header-substituted", r != nil && r.Host == labHost && r.Headers["x-api-key"] == digest(rootCanary))

	// Same variable name in two components: the values and markers differ,
	// and each header carried exactly its own component's value.
	nodeRecord, pythonRecord := find(*dir, "/node-api"), find(*dir, "/python-root")
	bleedFree := rootCanary != apiCanary && markerRoot != markerAPI &&
		nodeRecord != nil && pythonRecord != nil &&
		nodeRecord.Headers["x-api-key"] != digest(rootCanary) &&
		pythonRecord.Headers["x-api-key"] != digest(apiCanary) &&
		nodeRecord.Headers["x-api-key"] != pythonRecord.Headers["x-api-key"]
	result("components-do-not-bleed", bleedFree)

	// A request to a destination neither binding approved must fail closed,
	// and the lab must have no record of it.
	_, evilOK := runApp("python3", "/p3app/python/app.py", rootPath, fmt.Sprintf("https://%s:%d/evil-direct", evilHost, tls))
	result("evil-denied-failclosed", !evilOK && find(*dir, "/evil-direct") == nil)

	// The persisted sandbox config references the host transport variables
	// and never a raw canary, and configures no host mounts.
	fresh, e := msb.GetSandbox(ctx, name)
	if e != nil {
		diagnostic("persisted-config-get", e, "")
		result("persisted-envrefs-no-raw-canary", false)
		result("no-host-mounts", false)
		return
	}
	raw := fresh.ConfigJSON()
	cfg, cfgErr := fresh.Config()
	diagnostic("persisted-config-decode", cfgErr, "")
	result("persisted-envrefs-no-raw-canary", strings.Contains(raw, hostEnvRoot) && strings.Contains(raw, hostEnvAPI) &&
		!strings.Contains(raw, rootCanary) && !strings.Contains(raw, apiCanary))
	result("no-host-mounts", cfgErr == nil && len(cfg.Volumes) == 0)
}

func main() {
	run()
	if failed {
		os.Exit(1)
	}
}

// kitQuote wraps a value as a single-quoted POSIX shell word.
func kitQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// pathDir returns the parent directory of a POSIX guest path.
func pathDir(guestPath string) string {
	idx := strings.LastIndex(guestPath, "/")
	if idx <= 0 {
		return "/"
	}
	return guestPath[:idx]
}
