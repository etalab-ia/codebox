package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixtureDerivationsAndDestinations(t *testing.T) {
	if markerRoot == markerAPI {
		t.Fatal("the same variable name in two components must still yield distinct markers")
	}
	for _, m := range []string{markerRoot, markerAPI} {
		// The vendored renderer is pinned byte-identical to the pre-rename
		// commit f483a800 (verify_vendored.py), so its markers keep the
		// historical __JUST_CODE_SECRET_ prefix. The shape, not the product
		// name, is what this fixture asserts.
		if strings.Contains(m, "$") || !strings.HasPrefix(m, "__JUST_CODE_SECRET_") || !strings.HasSuffix(m, "__") {
			t.Fatalf("marker %q is not an opaque dollar-free placeholder", m)
		}
	}
	if guestEnvRoot != "JC_API_KEY" || guestEnvAPI != "JC_APPS_API_API_KEY" {
		t.Fatalf("guest env derivation drifted: %q %q", guestEnvRoot, guestEnvAPI)
	}
	if hostEnvRoot != "CODEBOX_HOST_PROJECT_JC_API_KEY" || hostEnvAPI != "CODEBOX_HOST_PROJECT_JC_APPS_API_API_KEY" {
		t.Fatalf("host transport names drifted: %q %q", hostEnvRoot, hostEnvAPI)
	}
	ds := deliveries()
	if len(ds) != 2 || ds[0].Name != secretName || ds[1].Name != secretName || ds[0].Component == ds[1].Component {
		t.Fatalf("fixture must share one variable name across two components: %+v", ds)
	}
	for _, d := range ds {
		if len(d.Destinations) != 1 || d.Destinations[0] != labHost {
			t.Fatalf("each binding must carry its own lab-only destination list: %+v", d)
		}
		if d.GuestEnv == "" || d.Marker == "" || d.Reference == "" {
			t.Fatalf("delivery incomplete: %+v", d)
		}
		if strings.Contains(d.Reference, "canary") {
			t.Fatal("references are opaque store ids, never values")
		}
	}
}

func TestDiagnosticRedactionAndResultFormat(t *testing.T) {
	registerCanaries("canary-value-for-test")
	sensitive = append(sensitive, "tok", "abc")
	s := scrub("code=4 tok /tmp/abc/ Authorization: Bearer abc canary-value-for-test " + base64Of("canary-value-for-test"))
	for _, secret := range []string{"tok", "abc", "canary-value-for-test", base64Of("canary-value-for-test"), "Bearer"} {
		if strings.Contains(s, secret) {
			t.Fatalf("scrub leaked %q in %q", secret, s)
		}
	}
	if !strings.Contains(s, "[REDACTED-CANARY]") || !strings.Contains(s, "[REDACTED-AUTHORIZATION]") {
		t.Fatalf("markers missing: %q", s)
	}
	if source == "" || archiveSHA == "" || kitRev != "p3v2" {
		t.Fatal("metadata constants incomplete")
	}
}

func base64Of(v string) string {
	return base64.StdEncoding.EncodeToString([]byte(v))
}

func TestLabRecordStoresDigestOnly(t *testing.T) {
	r := record{Path: "/node-api", Host: "p3lab.test", Headers: map[string]string{"x-api-key": digest("canary")}}
	b, _ := json.Marshal(r)
	line := string(b) + "\n"
	if strings.Contains(line, "canary") {
		t.Fatal("record leaked a raw value")
	}
	if !strings.Contains(line, digest("canary")) {
		t.Fatal("digest missing")
	}
}

func TestFindIsolatesByPath(t *testing.T) {
	d := t.TempDir()
	line := `{"path":"/node-api","host":"p3lab.test","headers":{"x-api-key":"abc"}}` + "\n"
	if err := os.WriteFile(filepath.Join(d, "requests.jsonl"), []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
	if find(d, "/node-api") == nil || find(d, "/python-root") != nil {
		t.Fatal("find must match the exact lab path")
	}
}

// The embedded fixtures are the exact pinned dependency set the probe
// installs inside the disposable guest.
func TestGuestFixtureDependencyPins(t *testing.T) {
	pkg, err := guestFixtures.ReadFile("guest/node-app/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(pkg, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Dependencies["dotenv"] != "18.0.9" || manifest.Dependencies["dotenv-expand"] != "13.0.0" {
		t.Fatalf("node loader pins drifted: %v", manifest.Dependencies)
	}
	lock, err := guestFixtures.ReadFile("guest/node-app/package-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lockfile struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(lock, &lockfile); err != nil {
		t.Fatal(err)
	}
	if lockfile.Packages["node_modules/dotenv"].Version != "18.0.9" || lockfile.Packages["node_modules/dotenv-expand"].Version != "13.0.0" {
		t.Fatalf("lockfile pins drifted")
	}
	req, err := guestFixtures.ReadFile("guest/python-app/requirements.txt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(req)) != "python-dotenv==1.2.4" {
		t.Fatalf("python loader pin drifted: %q", req)
	}
	for _, path := range []string{"guest/node-app/app.mjs", "guest/python-app/app.py"} {
		if _, err := guestFixtures.ReadFile(path); err != nil {
			t.Fatalf("embedded fixture missing: %s", path)
		}
	}
}
