package secrets

import (
	"errors"
	"strings"
	"testing"
)

func TestProjectIDRoundTripAndValidation(t *testing.T) {
	id, err := NewProjectID()
	if err != nil {
		t.Fatalf("NewProjectID() unexpected error: %v", err)
	}
	if err := ValidateProjectID(id); err != nil {
		t.Fatalf("ValidateProjectID(%q) rejected generated ID: %v", id, err)
	}

	invalid := []string{
		"",
		"not-a-uuid",
		strings.ToUpper(id),
		"../acl/" + id[:29],
		"00000000-0000-1000-8000-000000000000", // UUIDv1, not v4
		"00000000-0000-4000-0000-000000000000", // non-RFC4122 variant
		id + "\n",
	}
	for _, tc := range invalid {
		if err := ValidateProjectID(tc); !errors.Is(err, ErrInvalidProjectID) {
			t.Errorf("ValidateProjectID(%q) = %v, want ErrInvalidProjectID", tc, err)
		}
	}
}

func TestRejectProductionScope(t *testing.T) {
	allowed := []string{"", "dev", "development", "local", "preview", "sandbox"}
	for _, s := range allowed {
		if err := RejectProductionScope(s); err != nil {
			t.Errorf("RejectProductionScope(%q) unexpected error: %v", s, err)
		}
	}

	forbidden := []string{
		"prod", "PROD", " production ", "prd", "live",
		"prod-eu", "production_fr", "api-prod", "app_production",
	}
	for _, s := range forbidden {
		if err := RejectProductionScope(s); !errors.Is(err, ErrProductionScopeForbidden) {
			t.Errorf("RejectProductionScope(%q) = %v, want ErrProductionScopeForbidden", s, err)
		}
	}
}

func TestBundleMarshalUnmarshalAndLimits(t *testing.T) {
	longVal := strings.Repeat("x", 2048) // >951 bytes WinCred cap, valid in remote bundles
	input := map[string]map[string]string{
		".env": {
			"OPENAI_API_KEY": longVal,
		},
		"apps/api/.env.local": {
			"DATABASE_URL": "postgres://dev:dev@localhost:5432/app",
		},
		"empty/.env": {},
	}

	raw, err := MarshalBundle(input)
	if err != nil {
		t.Fatalf("MarshalBundle() unexpected error: %v", err)
	}

	got, err := UnmarshalBundle(raw)
	if err != nil {
		t.Fatalf("UnmarshalBundle() unexpected error: %v", err)
	}
	if got.Components[".env"]["OPENAI_API_KEY"] != longVal {
		t.Fatalf("decoded OPENAI_API_KEY length = %d, want %d", len(got.Components[".env"]["OPENAI_API_KEY"]), len(longVal))
	}
	if _, exists := got.Components["empty/.env"]; exists {
		t.Fatalf("expected empty component map to be omitted on marshal")
	}

	// Exceeding 60 KiB fails closed with ErrBundleTooLarge.
	oversized := map[string]map[string]string{
		".env": {"HUGE_SECRET": strings.Repeat("a", MaxBundleBytes+1)},
	}
	if _, err := MarshalBundle(oversized); !errors.Is(err, ErrBundleTooLarge) {
		t.Fatalf("MarshalBundle(oversized) = %v, want ErrBundleTooLarge", err)
	}
	if _, err := UnmarshalBundle([]byte(strings.Repeat(" ", MaxBundleBytes+1))); !errors.Is(err, ErrBundleTooLarge) {
		t.Fatalf("UnmarshalBundle(oversized) = %v, want ErrBundleTooLarge", err)
	}

	// Path traversal, NUL values, and unknown JSON fields fail closed.
	if _, err := MarshalBundle(map[string]map[string]string{"../.env": {"KEY": "v"}}); err == nil {
		t.Fatal("expected parent traversal component path to fail")
	}
	if _, err := MarshalBundle(map[string]map[string]string{".env": {"KEY": "bad\x00val"}}); err == nil {
		t.Fatal("expected NUL byte in secret value to fail")
	}
	if _, err := UnmarshalBundle([]byte(`{"schemaVersion":1,"components":{},"extra":true}`)); err == nil {
		t.Fatal("expected unknown JSON field to be rejected")
	}
	if _, err := UnmarshalBundle([]byte(`{"schemaVersion":1,"components":{}}   ` + "\n")); err != nil {
		t.Fatalf("expected trailing JSON whitespace to succeed, got %v", err)
	}
	for _, trailing := range []string{
		`{"schemaVersion":1,"components":{}}]`,
		`{"schemaVersion":1,"components":{}}}garbage`,
		`{"schemaVersion":1,"components":{}}{}`,
	} {
		if _, err := UnmarshalBundle([]byte(trailing)); err == nil {
			t.Errorf("expected trailing payload %q to be rejected", trailing)
		}
	}
}

func TestValidateAndCanonicalizeBindingHints(t *testing.T) {
	hints := []BindingHint{
		{
			Component:    "apps/api/.env",
			Name:         "API_TOKEN",
			Delivery:     DeliveryProxy,
			Destinations: []string{"z.example.gouv.fr", "a.example.gouv.fr"},
		},
		{
			Component: ".env",
			Name:      "DB_PASSWORD",
			Delivery:  DeliveryGuest,
		},
	}
	if err := ValidateBindingHints(hints); err != nil {
		t.Fatalf("ValidateBindingHints() unexpected error: %v", err)
	}
	canon := CanonicalizeBindingHints(hints)
	if canon[0].Component != ".env" || canon[0].Delivery != DeliveryGuest {
		t.Fatalf("unexpected first canonical hint: %+v", canon[0])
	}
	if canon[1].Delivery != "" || canon[1].Destinations[0] != "a.example.gouv.fr" {
		t.Fatalf("expected default proxy omitted and destinations sorted, got %+v", canon[1])
	}

	badCases := [][]BindingHint{
		{{Component: ".env", Name: "KEY", Delivery: DeliveryGuest, Destinations: []string{"api.example.com"}}},
		{{Component: ".env", Name: "KEY", Delivery: DeliveryProxy}},
		{{Component: ".env", Name: "KEY", Destinations: []string{"https://api.example.com"}}},
		{{Component: ".env", Name: "KEY", Destinations: []string{"127.0.0.1"}}},
		{{Component: ".env", Name: "KEY", Destinations: []string{"*.example.com"}}},
		{
			{Component: ".env", Name: "A", GuestEnv: "SHARED_ENV", Destinations: []string{"a.example.com"}},
			{Component: "sub/.env", Name: "B", GuestEnv: "SHARED_ENV", Destinations: []string{"b.example.com"}},
		},
	}
	for i, tc := range badCases {
		if err := ValidateBindingHints(tc); err == nil {
			t.Errorf("badCases[%d] unexpectedly succeeded", i)
		}
	}
}
