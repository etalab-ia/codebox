package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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

const source = "3b853c61b95659c99769eda485dc7791cb44ea14"
const archiveSHA = "9cc2bd2874cc1aa98cb139429a9482830b1e64b48255aa9f40a25b8979a23a9f"
const markerA = "__JUST_CODE_SECRET_a01__"
const markerB = "__JUST_CODE_SECRET_b02__"
const markerC = "__JUST_CODE_SECRET_c03__"

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
func spec(env, marker string) msb.SecretModifySpec {
	return msb.SecretModifySpec{Env: env, Placeholder: marker, AllowedHosts: []string{"p02lab.test"}}
}
func specC() msb.SecretModifySpec {
	return msb.SecretModifySpec{Env: "JC_P0_HOST_C", Placeholder: markerC, AllowedHosts: []string{"evil.test"}}
}
func run() {
	dir := flag.String("dir", "", "owned run directory")
	ip := flag.String("ip", "", "host LAN IP supplied by the test runner")
	tlsPort := flag.Int("tls", 18443, "TLS port")
	dnsPort := flag.Int("dns", 15354, "DNS port")
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
		if !strings.HasPrefix(name, "jc-p0-") {
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
	fmt.Printf("META kit=v3 source=%s sdk=0.7.14 runtime=0.7.14 archive_sha256=%s go=%s\n", source, archiveSHA, runtime.Version())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	name := "jc-p0-" + random()[:16]
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
	a, b, a2, cValue := random(), random(), random(), random()
	registerCanaries(a, b, a2, cValue)
	_ = os.Setenv("JC_P0_HOST_C", cValue)
	defer os.Unsetenv("JC_P0_HOST_C")
	_ = os.Setenv("JC_P0_HOST_A", a)
	_ = os.Setenv("JC_P0_HOST_B", b)
	defer os.Unsetenv("JC_P0_HOST_A")
	defer os.Unsetenv("JC_P0_HOST_B")
	no := false
	net := msb.NetworkPolicy.AllowAll()
	net.TrustHostCAs = &no
	net.DNS = &msb.DNSConfig{Nameservers: []string{fmt.Sprintf("%s:%d", *ip, *dnsPort)}, RebindProtection: &no}
	net.TLS = &msb.TLSConfig{InterceptedPorts: []uint16{uint16(*tlsPort)}, CACert: filepath.Join(*dir, "ca.crt"), CAKey: filepath.Join(*dir, "ca.key"), UpstreamCACerts: []string{filepath.Join(*dir, "ca.crt")}}
	s, e := msb.CreateSandbox(ctx, name, msb.WithImage("docker.io/library/python:3.12-alpine"), msb.WithCPUs(2), msb.WithMemory(768), msb.WithDetached(), msb.WithMaxDuration(6*time.Minute), msb.WithEntrypoint("/bin/sh", "-c"), msb.WithCmd("sleep 360"), msb.WithNetwork(net), msb.WithSecrets(msb.Secret.Env("JC_A", "$MSB_BOOTSTRAP_UNSET", msb.SecretEnvOptions{Allow: []string{"p02lab.test"}, Placeholder: markerA}), msb.Secret.Env("JC_B", "$MSB_BOOTSTRAP_UNSET", msb.SecretEnvOptions{Allow: []string{"p02lab.test"}, Placeholder: markerB}), msb.Secret.Env("JC_C", "$MSB_BOOTSTRAP_UNSET", msb.SecretEnvOptions{Allow: []string{"evil.test"}, Placeholder: markerC})))
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
	modify := func(stage string, m msb.ModifyOptions) bool {
		p, e := h.Modify(ctx, m)
		diagnostic(stage, e, "")
		if e == nil && (p == nil || !p.Applied) {
			detail, _ := json.Marshal(p)
			diagnostic(stage, nil, "unapplied modification plan: "+string(detail))
		}
		return e == nil && p != nil && p.Applied
	}
	ok := modify("rotate-to-envrefs", msb.ModifyOptions{Policy: msb.ModificationPolicyNoRestart, Secrets: map[string]msb.SecretModifySpec{"JC_A": spec("JC_P0_HOST_A", markerA), "JC_B": spec("JC_P0_HOST_B", markerB), "JC_C": specC()}})
	result("rotate-to-envrefs", ok)
	if !ok {
		return
	}
	exec := func(code string) (*msb.ExecOutput, error) {
		c, d := context.WithTimeout(ctx, 25*time.Second)
		defer d()
		out, err := s.Exec(c, "python3", []string{"-c", code}, msb.WithExecTimeout(20*time.Second))
		diagnostic("guest-exec", err, "")
		if err == nil && out != nil && !out.Success() {
			diagnostic("guest-exec", nil, fmt.Sprintf("exit_code=%d stderr=%s", out.ExitCode(), out.Stderr()))
		}
		return out, err
	}
	out, e := exec("import os,json; print(json.dumps([os.environ.get('JC_A'),os.environ.get('JC_B'),os.environ.get('JC_C')]))")
	var env []string
	if e == nil {
		diagnostic("guest-env-json", json.Unmarshal([]byte(out.Stdout()), &env), "")
	}
	result("exact-guest-markers-no-canary", e == nil && len(env) == 3 && env[0] == markerA && env[1] == markerB && env[2] == markerC)
	request := func(host, path, headers string, redirect bool) bool {
		code := fmt.Sprintf("import os,ssl,urllib.request,base64\nu='https://%s:%d%s'\nh=%s\nr=urllib.request.Request(u,headers=h)\n", host, *tlsPort, path, headers)
		if !redirect {
			code += "class NoRedirect(urllib.request.HTTPRedirectHandler):\n def redirect_request(self,*a,**k): return None\n"
		}
		code += "ctx=ssl._create_unverified_context()\n"
		if redirect {
			code += "urllib.request.urlopen(r,context=ctx,timeout=12).read()"
		} else {
			code += "urllib.request.build_opener(NoRedirect(),urllib.request.HTTPSHandler(context=ctx)).open(r,timeout=12).read()"
		}
		o, e := exec(code)
		if e != nil || (o != nil && !o.Success()) {
			diagnostic("request"+path, e, "guest request did not succeed")
		}
		return e == nil && o != nil && o.Success()
	}
	headers := "{'X-A':os.environ['JC_A'],'X-B':os.environ['JC_B']}"
	cHeaders := "{'X-C':os.environ['JC_C']}"
	reached := request("evil.test", "/c-positive", cHeaders, true)
	cr := find(*dir, "/c-positive")
	result("c-only-evil-positive-reachability", reached && cr != nil && cr.Host == "evil.test" && cr.Headers["x-c"] == digest(cValue))
	cBlocked := !request("p02lab.test", "/c-wrong-host", cHeaders, true)
	result("c-not-authorized-by-ab-p02lab", cBlocked && find(*dir, "/c-wrong-host") == nil)
	// C is already registered and positively reached evil.test: A/B must not
	// inherit C's authorization merely because a different binding permits it.
	abBlocked := !request("evil.test", "/ab-wrong-host", headers, true)
	result("ab-not-authorized-by-c-evil", abBlocked && find(*dir, "/ab-wrong-host") == nil)
	request("p02lab.test", "/headers", headers, true)
	r := find(*dir, "/headers")
	result("shared-host-two-header-substitutions", r != nil && r.Headers["x-a"] == digest(a) && r.Headers["x-b"] == digest(b))
	basic := "{'Authorization':'Basic '+base64.b64encode(('user:'+os.environ['JC_A']).encode()).decode()}"
	request("p02lab.test", "/basic", basic, true)
	r = find(*dir, "/basic")
	result("basic-base64-substitution", r != nil && r.Headers["authorization"] == digest("Basic "+base64.StdEncoding.EncodeToString([]byte("user:"+a))))
	blocked := !request("evil.test", "/evil-direct", headers, true)
	result("disallowed-evil-failclosed", blocked && find(*dir, "/evil-direct") == nil)
	blocked = !request("p02lab.test", "/redirect-to-evil", headers, true)
	r = find(*dir, "/redirect-to-evil")
	result("allowed-to-evil-redirect-failclosed", blocked && r != nil && r.Headers["x-a"] == digest(a) && find(*dir, "/redirect-target") == nil)
	_ = os.Setenv("JC_P0_HOST_A", a2)
	ok = modify("live-rotation", msb.ModifyOptions{Policy: msb.ModificationPolicyNoRestart, Secrets: map[string]msb.SecretModifySpec{"JC_A": spec("JC_P0_HOST_A", markerA)}})
	request("p02lab.test", "/rotation", headers, true)
	r = find(*dir, "/rotation")
	result("live-rotation-other-binding-unchanged", ok && r != nil && r.Headers["x-a"] == digest(a2) && r.Headers["x-b"] == digest(b))
	fresh, e := msb.GetSandbox(ctx, name)
	if e != nil {
		diagnostic("persisted-config-get", e, "")
		result("persisted-config", false)
	} else {
		raw := fresh.ConfigJSON()
		cfg, e := fresh.Config()
		diagnostic("persisted-config-decode", e, "")
		result("persisted-envrefs-no-raw-canary", strings.Contains(raw, "JC_P0_HOST_A") && strings.Contains(raw, "JC_P0_HOST_B") && strings.Contains(raw, "JC_P0_HOST_C") && !strings.Contains(raw, cValue) && !strings.Contains(raw, a) && !strings.Contains(raw, b) && !strings.Contains(raw, a2))
		result("no-host-mounts", e == nil && len(cfg.Volumes) == 0)
	}
	ok = modify("removal", msb.ModifyOptions{Policy: msb.ModificationPolicyNoRestart, SecretsRemove: []string{"JC_A"}})
	request("p02lab.test", "/removed", headers, true)
	r = find(*dir, "/removed")
	safe := r == nil || (r.Headers["x-a"] != digest(a) && r.Headers["x-a"] != digest(a2))
	result("removal-no-old-secret-transmission", ok && safe)
	request("p02lab.test", "/remaining", "{'X-B':os.environ['JC_B']}", true)
	r = find(*dir, "/remaining")
	result("removal-other-binding-still-works", r != nil && r.Headers["x-b"] == digest(b))
	// A's proxy registration is removed. Re-adding it changes boot-time guest
	// environment and must refuse NoRestart, rather than silently hot-add.
	readd := msb.ModifyOptions{Policy: msb.ModificationPolicyNoRestart, Secrets: map[string]msb.SecretModifySpec{"JC_A": spec("JC_P0_HOST_A", markerA)}}
	plan, addErr := h.Modify(ctx, readd)
	diagnostic("live-readd-refusal", addErr, "")
	refused := addErr != nil && strings.Contains(strings.ToLower(addErr.Error()), "requires restart") && strings.Contains(addErr.Error(), "JC_A")
	if plan != nil {
		detail, _ := json.Marshal(plan)
		diagnostic("live-readd-plan", nil, string(detail))
	}
	result("live-readd-requires-restart", refused)
	request("p02lab.test", "/after-refused-readd", headers, true)
	r = find(*dir, "/after-refused-readd")
	safe = r == nil || (r.Headers["x-a"] != digest(a) && r.Headers["x-a"] != digest(a2))
	result("refused-readd-does-not-reactivate-a", refused && safe)
	readd.Policy = msb.ModificationPolicyNextStart
	queued := modify("nextstart-readd", readd)
	result("nextstart-readd-persisted", queued)
	if !queued {
		return
	}
	request("p02lab.test", "/queued-not-live", headers, true)
	r = find(*dir, "/queued-not-live")
	safe = r == nil || (r.Headers["x-a"] != digest(a) && r.Headers["x-a"] != digest(a2))
	result("nextstart-readd-not-effective-until-boot", safe)
	stopCtx, stopDone := context.WithTimeout(ctx, 30*time.Second)
	stopErr := s.StopWithTimeout(stopCtx, 20*time.Second)
	stopDone()
	diagnostic("nextboot-stop", stopErr, "")
	result("nextboot-stop", stopErr == nil)
	if stopErr != nil {
		return
	}
	detachErr := s.Detach(ctx)
	diagnostic("nextboot-release-old-handle", detachErr, "")
	result("nextboot-release-old-handle", detachErr == nil)
	if detachErr != nil {
		return
	}
	h, e = msb.GetSandbox(ctx, name)
	diagnostic("nextboot-get", e, "")
	if e != nil {
		result("nextboot-start", false)
		return
	}
	s, e = h.StartDetached(ctx)
	diagnostic("nextboot-start", e, "")
	result("nextboot-start", e == nil)
	if e != nil {
		return
	}
	// StartDetached returns a connected Sandbox. The exec closure follows s.
	out, e = exec("import os,json; print(json.dumps([os.environ.get('JC_A'),os.environ.get('JC_B'),os.environ.get('JC_C')]))")
	env = nil
	if e == nil && out != nil {
		diagnostic("nextboot-env-json", json.Unmarshal([]byte(out.Stdout()), &env), "")
	}
	result("nextboot-exact-three-markers", e == nil && len(env) == 3 && env[0] == markerA && env[1] == markerB && env[2] == markerC)
	reached = request("p02lab.test", "/nextboot-ab", headers, true)
	r = find(*dir, "/nextboot-ab")
	result("nextboot-restored-rotated-a-and-unchanged-b", reached && r != nil && r.Headers["x-a"] == digest(a2) && r.Headers["x-b"] == digest(b))
	reached = request("evil.test", "/nextboot-c", cHeaders, true)
	r = find(*dir, "/nextboot-c")
	result("nextboot-c-still-independent", reached && r != nil && r.Headers["x-c"] == digest(cValue))
	blocked = !request("evil.test", "/nextboot-ab-wrong-host", headers, true)
	result("nextboot-ab-still-denied-evil", blocked && find(*dir, "/nextboot-ab-wrong-host") == nil)
	cBlocked = !request("p02lab.test", "/nextboot-c-wrong-host", cHeaders, true)
	result("nextboot-c-still-denied-p02lab", cBlocked && find(*dir, "/nextboot-c-wrong-host") == nil)
	fresh, e = msb.GetSandbox(ctx, name)
	diagnostic("nextboot-config-get", e, "")
	if e == nil {
		raw := fresh.ConfigJSON()
		cfg, decodeErr := fresh.Config()
		diagnostic("nextboot-config-decode", decodeErr, "")
		result("nextboot-persisted-envrefs-no-canary-no-mounts", strings.Contains(raw, "JC_P0_HOST_A") && strings.Contains(raw, "JC_P0_HOST_B") && strings.Contains(raw, "JC_P0_HOST_C") && !strings.Contains(raw, a) && !strings.Contains(raw, a2) && !strings.Contains(raw, b) && !strings.Contains(raw, cValue) && decodeErr == nil && len(cfg.Volumes) == 0)
	} else {
		result("nextboot-persisted-envrefs-no-canary-no-mounts", false)
	}
}
func main() {
	run()
	if failed {
		os.Exit(1)
	}
}
