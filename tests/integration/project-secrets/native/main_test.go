package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestMarkersAndDistinctSharedDestination(t *testing.T) {
	if markerA == markerB {
		t.Fatal("markers must differ")
	}
	for _, m := range []string{markerA, markerB, markerC} {
		if strings.Contains(m, "$") || !strings.HasPrefix(m, "__CODEBOX_SECRET_") || !strings.HasSuffix(m, "__") {
			t.Fatal("invalid opaque marker")
		}
	}
	a, b := spec("JC_P0_HOST_A", markerA), spec("JC_P0_HOST_B", markerB)
	if a.Env == b.Env || a.Placeholder == b.Placeholder || len(a.AllowedHosts) != 1 || a.AllowedHosts[0] != b.AllowedHosts[0] {
		t.Fatal("invalid two-binding contract")
	}
	if a.Value != "" || b.Value != "" {
		t.Fatal("env references must not contain raw value")
	}
}
func TestThirdBindingHasIndependentDestination(t *testing.T) {
	c := specC()
	a := spec("JC_P0_HOST_A", markerA)
	if c.Env == a.Env || c.Placeholder == a.Placeholder || c.Placeholder == markerB || c.Value != "" || len(c.AllowedHosts) != 1 || c.AllowedHosts[0] != "evil.test" || c.AllowedHosts[0] == a.AllowedHosts[0] {
		t.Fatal("C must authorize only evil.test independently")
	}
}
func TestDiagnosticRedaction(t *testing.T) {
	old := sensitive
	defer func() { sensitive = old }()
	sensitive = nil
	value := "synthetic-canary-v2"
	registerCanaries(value)
	encoded := base64.StdEncoding.EncodeToString([]byte("user:" + value))
	got := scrub("invalid config: socket path too long\nraw=" + value + " encoded=" + encoded + "\nAuthorization: Basic arbitrary-token\nBearer another-token")
	for _, forbidden := range []string{value, encoded, "arbitrary-token", "another-token"} {
		if strings.Contains(got, forbidden) {
			t.Fatal("diagnostic leaked sensitive value")
		}
	}
	if !strings.Contains(got, "socket path too long") {
		t.Fatal("useful error text lost")
	}
}
func TestLabRecordStoresDigest(t *testing.T) {
	raw := `{"path":"/headers","headers":{"x-a":"` + digest("synthetic") + `"}}`
	var r record
	if json.Unmarshal([]byte(raw), &r) != nil || r.Headers["x-a"] != digest("synthetic") {
		t.Fatal("record shape")
	}
	if strings.Contains(raw, "synthetic") {
		t.Fatal("raw canary leaked")
	}
}
