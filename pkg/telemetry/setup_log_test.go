package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupEventWritesOnboardLogAndRedactsSecrets(t *testing.T) {
	home := t.TempDir()
	SetupEvent(home, "setup.start", map[string]any{
		"provider":    "openai",
		"api_key":     "sk-live-secret",
		"api_key_set": true,
		"base_url":    "http://10.20.200.100:30122/v1",
		"auth":        "Bearer raw-token",
	})

	raw, err := os.ReadFile(filepath.Join(home, "logs", SetupLogFile))
	if err != nil {
		t.Fatalf("read onboard log: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"event=setup.start",
		`provider="openai"`,
		`api_key="[REDACTED]"`,
		`api_key_set="true"`,
		`base_url="http://10.20.200.100:30122/v1"`,
		`auth="Bearer [REDACTED]"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("log missing %q: %s", want, text)
		}
	}
	if strings.Contains(text, "sk-live-secret") || strings.Contains(text, "raw-token") {
		t.Fatalf("log leaked secret: %s", text)
	}
	if _, err := os.Stat(filepath.Join(home, "logs", "onboard.log")); !os.IsNotExist(err) {
		t.Fatalf("expected onboard.log absent, stat err=%v", err)
	}
}
