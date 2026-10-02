package process

import (
	"path/filepath"
	"testing"

	"github.com/joho/godotenv"
)

// A hyphenated provider id must still name a legal env variable: the .env
// parser rejects "-" in names, and one bad line takes the whole file down
// with it (the gateway's hot reload included).
func TestProviderAPIKeyConfigReferenceNormalizesHyphenatedProviders(t *testing.T) {
	home := t.TempDir()
	ref, err := ProviderAPIKeyConfigReference(home, "e2e-svc", "sk-test-1234")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "${E2E_SVC_API_KEY}" {
		t.Fatalf("reference = %q", ref)
	}
	env, err := godotenv.Read(filepath.Join(home, ".env"))
	if err != nil {
		t.Fatalf("the written .env must parse: %v", err)
	}
	if env["E2E_SVC_API_KEY"] != "sk-test-1234" {
		t.Fatalf("stored key = %q", env["E2E_SVC_API_KEY"])
	}
}
