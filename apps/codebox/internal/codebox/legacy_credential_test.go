package codebox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLegacyAlbertCredentialReadsLiteralWithoutShellExpansion(t *testing.T) {
	value, ok := ReadLegacyAlbertCredential("# ignored\nALBERT_API_KEY=first-key_123\n")
	if !ok || value != "first-key_123" {
		t.Fatalf("literal credential = %q, %v", value, ok)
	}
	duplicate := "ALBERT_API_KEY=$(command)\nALBERT_API_KEY=later-literal\n"
	if _, ok := ReadLegacyAlbertCredential(duplicate); ok || !LegacyAlbertCredentialNeedsManualReview(duplicate) {
		t.Fatal("a later literal must not override an earlier unsupported or repeated assignment")
	}
	sentinel := filepath.Join(t.TempDir(), "executed")
	if _, ok := ReadLegacyAlbertCredential("ALBERT_API_KEY=$(touch " + sentinel + ")\n"); ok {
		t.Fatal("shell expression was accepted as a credential value")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("credential import executed a shell expression")
	}
	if _, ok := ReadLegacyAlbertCredential("OTHER_TOKEN=secret\n"); ok {
		t.Fatal("unrelated credential-shaped variable was accepted as the Albert key")
	}
	if _, ok := ReadLegacyAlbertCredential("ALBERT_API_KEY=\n"); ok {
		t.Fatal("empty Albert key was accepted")
	}
	if _, ok := ReadLegacyAlbertCredential("ALBERT_API_KEY=key # inline comment\n"); ok {
		t.Fatal("ambiguous inline-comment value was accepted")
	}
	preview := ImportLegacyDotenv("ALBERT_API_KEY=secret-bytes\n").FormatLegacyImport()
	if strings.Contains(preview, "secret-bytes") {
		t.Fatal("existing dotenv preview leaked the credential")
	}
}
