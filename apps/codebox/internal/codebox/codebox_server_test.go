package codebox

import (
	"errors"
	"strings"
	"testing"
)

func TestCodeboxServerURL(t *testing.T) {
	oldDefault := CodeboxServerURLDefault
	t.Cleanup(func() { CodeboxServerURLDefault = oldDefault })

	for _, tc := range []struct {
		name    string
		env     string
		def     string
		want    string
		wantErr bool
	}{
		{name: "unconfigured", want: ""},
		{name: "default", def: "https://gateway.example/", want: "https://gateway.example"},
		{name: "env overrides default", env: "https://from-env.example", def: "https://from-build.example", want: "https://from-env.example"},
		{name: "http refused", env: "http://gateway.example", wantErr: true},
		{name: "userinfo refused", env: "https://user:pass@gateway.example", wantErr: true},
		{name: "query refused", env: "https://gateway.example/?x=1", wantErr: true},
		{name: "fragment refused", env: "https://gateway.example/#x", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CODEBOX_SERVER_URL", tc.env)
			CodeboxServerURLDefault = tc.def
			got, err := CodeboxServerURL()
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidCodeboxServerURL) {
					t.Fatalf("error = %v, want ErrInvalidCodeboxServerURL", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("URL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCodeboxServerCredentialKindIsNotGenericAuth(t *testing.T) {
	if IsKnownCredentialKind(string(CredentialCodeboxServer)) {
		t.Fatal("the gateway session token must not be writable through generic codebox auth")
	}
	if service := credentialService(CredentialCodeboxServer); !strings.Contains(service, string(CredentialCodeboxServer)) {
		t.Fatalf("credential service = %q, want a dedicated service name", service)
	}
}
