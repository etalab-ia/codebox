package codebox

import (
	"errors"
	"strings"
	"testing"
)

func TestSecretsGatewayURL(t *testing.T) {
	oldDefault := SecretsGatewayURLDefault
	t.Cleanup(func() { SecretsGatewayURLDefault = oldDefault })

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
			t.Setenv("CODEBOX_SECRETS_GATEWAY_URL", tc.env)
			SecretsGatewayURLDefault = tc.def
			got, err := SecretsGatewayURL()
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidSecretsGatewayURL) {
					t.Fatalf("error = %v, want ErrInvalidSecretsGatewayURL", err)
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

func TestSecretsGatewayCredentialKindIsNotGenericAuth(t *testing.T) {
	if IsKnownCredentialKind(string(CredentialSecretsGateway)) {
		t.Fatal("the gateway session token must not be writable through generic codebox auth")
	}
	if service := credentialService(CredentialSecretsGateway); !strings.Contains(service, string(CredentialSecretsGateway)) {
		t.Fatalf("credential service = %q, want a dedicated service name", service)
	}
}
