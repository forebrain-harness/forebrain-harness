package process

import (
	"context"
	"os"
	"regexp"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

var generatedGatewayTokenRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// gatewayTokenHomeForTest is a home whose configuration file holds body, in a
// process that has generated no token yet.
func gatewayTokenHomeForTest(t *testing.T, body string) (home, cfgPath string) {
	t.Helper()
	home, cfgPath, _ = dotEnvHomeForTest(t)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	appcfg.SetGeneratedGatewayToken("")
	ResetResolve()
	t.Cleanup(func() {
		appcfg.SetGeneratedGatewayToken("")
		ResetResolve()
	})
	return home, cfgPath
}

func effectiveGatewayTokenForTest(t *testing.T, home string) string {
	t.Helper()
	cfg, err := StartupConfig(context.Background(), home)
	if err != nil {
		t.Fatalf("startup config: %v", err)
	}
	return cfg.Gateway.Auth.Token
}

func configFileForTest(t *testing.T, cfgPath string) string {
	t.Helper()
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(raw)
}

// gateway.auth.token is the operator's to write, in plaintext: the gateway
// starts with exactly that value and generates nothing.
func TestEnsureGatewayTokenKeepsAHandWrittenToken(t *testing.T) {
	const body = `# my gateway
gateway:
  http_addr: "127.0.0.1:6060"
  auth:
    token: my-own-token
`
	home, cfgPath := gatewayTokenHomeForTest(t, body)
	generated, err := EnsureGatewayToken(context.Background(), home)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if generated != "" {
		t.Fatalf("generated %q; a configured token must not be replaced", generated)
	}
	if got := configFileForTest(t, cfgPath); got != body {
		t.Fatalf("the file was rewritten:\n%s", got)
	}
	if got := effectiveGatewayTokenForTest(t, home); got != "my-own-token" {
		t.Fatalf("token in effect = %q, want the hand-written one", got)
	}
}

// A reference something supplies is a configured token too.
func TestEnsureGatewayTokenKeepsAReferenceThatResolves(t *testing.T) {
	t.Setenv("MY_GATEWAY_TOKEN", "from-env")
	const body = "gateway:\n  auth:\n    token: ${MY_GATEWAY_TOKEN}\n"
	home, cfgPath := gatewayTokenHomeForTest(t, body)
	generated, err := EnsureGatewayToken(context.Background(), home)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if generated != "" {
		t.Fatalf("generated %q; a reference the environment supplies must stay in effect", generated)
	}
	if got := configFileForTest(t, cfgPath); got != body {
		t.Fatalf("the file was rewritten:\n%s", got)
	}
	if got := effectiveGatewayTokenForTest(t, home); got != "from-env" {
		t.Fatalf("token in effect = %q", got)
	}
}

// No token in effect — an empty key, no key, or a reference nothing resolves —
// and the gateway generates one for this process. The file is not touched: the
// token is the process's own, printed where the gateway was started.
func TestEnsureGatewayTokenGeneratesInMemoryWhenNoneIsInEffect(t *testing.T) {
	cases := map[string]string{
		"a reference nothing resolves": "# kept\ngateway:\n  http_addr: \"127.0.0.1:6060\"\n  auth:\n    token: ${NOBODY_SETS_THIS_TOKEN}\n",
		"an empty key":                 "# kept\ngateway:\n  http_addr: \"127.0.0.1:6060\"\n  auth:\n    token: \"\"\n",
		"a key with no value":          "# kept\ngateway:\n  http_addr: \"127.0.0.1:6060\"\n  auth:\n    token:\n",
		"no auth section":              "# kept\ngateway:\n  http_addr: \"127.0.0.1:6060\"\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			home, cfgPath := gatewayTokenHomeForTest(t, body)
			token, err := EnsureGatewayToken(context.Background(), home)
			if err != nil {
				t.Fatalf("ensure: %v", err)
			}
			if !generatedGatewayTokenRe.MatchString(token) {
				t.Fatalf("generated token = %q, want 64 hex characters", token)
			}
			if got := configFileForTest(t, cfgPath); got != body {
				t.Fatalf("the generated token must not be written to the file:\n%s", got)
			}
			// Every configuration this process reads from here on carries it.
			if got := effectiveGatewayTokenForTest(t, home); got != token {
				t.Fatalf("token in effect = %q, want the generated %q", got, token)
			}
			// Asked again, the process already has its token.
			again, err := EnsureGatewayToken(context.Background(), home)
			if err != nil {
				t.Fatalf("ensure again: %v", err)
			}
			if again != "" {
				t.Fatalf("generated a second token %q in one process", again)
			}

			// A token the operator then writes into the file takes over.
			if err := os.WriteFile(cfgPath, []byte("gateway:\n  auth:\n    token: written-later\n"), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if got := effectiveGatewayTokenForTest(t, home); got != "written-later" {
				t.Fatalf("token in effect = %q, want the one written into the file", got)
			}

			// The next process starts with no generated token of its own, and
			// generates a different one.
			if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			appcfg.SetGeneratedGatewayToken("")
			ResetResolve()
			next, err := EnsureGatewayToken(context.Background(), home)
			if err != nil {
				t.Fatalf("ensure in the next process: %v", err)
			}
			if !generatedGatewayTokenRe.MatchString(next) || next == token {
				t.Fatalf("next process's token = %q, want a fresh one (previous %q)", next, token)
			}
		})
	}
}
