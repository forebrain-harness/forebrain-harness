package home

import "testing"

func TestSafeSubprocessEnvStripsSecretsAndKeepsBaseRuntime(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/tmp/home",
		"OPENAI_API_KEY=secret",
		"AWS_SECRET_ACCESS_KEY=secret2",
		"LANG=en_US.UTF-8",
		"TERM=xterm-256color",
	}
	got := SafeSubprocessEnv(parent, Options{})
	joined := map[string]bool{}
	for _, item := range got {
		joined[item] = true
	}
	if !joined["PATH=/usr/bin"] || !joined["HOME=/tmp/home"] || !joined["LANG=en_US.UTF-8"] || !joined["TERM=xterm-256color"] {
		t.Fatalf("missing required runtime env: %#v", got)
	}
	for _, forbidden := range []string{"OPENAI_API_KEY=secret", "AWS_SECRET_ACCESS_KEY=secret2"} {
		if joined[forbidden] {
			t.Fatalf("forbidden env leaked: %s", forbidden)
		}
	}
}

func TestSafeSubprocessEnvRejectsSecretLikeExplicitEnvByDefault(t *testing.T) {
	got := SafeSubprocessEnv(nil, Options{
		ExplicitEnv: map[string]string{
			"FOREBRAIN_HOME":          "/tmp/home",
			"OPENAI_API_KEY":          "secret",
			"FOREBRAIN_SESSION_ID":    "s1",
			"FOREBRAIN_GATEWAY_TOKEN": "tok",
		},
	})
	joined := map[string]bool{}
	for _, item := range got {
		joined[item] = true
	}
	if !joined["FOREBRAIN_HOME=/tmp/home"] || !joined["FOREBRAIN_SESSION_ID=s1"] {
		t.Fatalf("expected non-secret explicit env: %#v", got)
	}
	if joined["OPENAI_API_KEY=secret"] || joined["FOREBRAIN_GATEWAY_TOKEN=tok"] {
		t.Fatalf("secret explicit env leaked: %#v", got)
	}
}

func TestSafeSubprocessEnvBranches(t *testing.T) {
	oldDefaultAllow := defaultAllow
	defaultAllow = append(append([]string{}, defaultAllow...), "OPENAI_API_KEY")
	t.Cleanup(func() { defaultAllow = oldDefaultAllow })

	parent := []string{
		"NO_EQUALS",
		" =blank",
		"PATH=",
		"PATH=/usr/bin",
		"OPENAI_API_KEY=secret",
		"UNLISTED=value",
	}
	got := SafeSubprocessEnv(parent, Options{
		AllowSecretEnv: true,
		ExplicitEnv: map[string]string{
			" ":             "skip",
			"SAFE_VISIBLE":  "ok",
			"benign_key_1":  "ok2",
			"bad-key":       "skip",
			"PRIVATE_TOKEN": "allowed",
		},
	})
	joined := map[string]bool{}
	for _, item := range got {
		joined[item] = true
	}
	if !joined["PATH=/usr/bin"] || !joined["OPENAI_API_KEY=secret"] ||
		!joined["SAFE_VISIBLE=ok"] || !joined["benign_key_1=ok2"] || !joined["PRIVATE_TOKEN=allowed"] {
		t.Fatalf("expected env values missing: %#v", got)
	}
	if joined["PATH="] || joined["UNLISTED=value"] {
		t.Fatalf("unexpected env leaked: %#v", got)
	}

	got = SafeSubprocessEnv(parent, Options{
		ExplicitEnv: map[string]string{
			"bad-key":       "skip",
			"PRIVATE_TOKEN": "skip",
		},
	})
	joined = map[string]bool{}
	for _, item := range got {
		joined[item] = true
	}
	if joined["OPENAI_API_KEY=secret"] || joined["bad-key=skip"] || joined["PRIVATE_TOKEN=skip"] {
		t.Fatalf("secret or unsafe explicit env leaked: %#v", got)
	}
}

func TestEnvKeyClassifiers(t *testing.T) {
	if looksSecretKey(" ") {
		t.Fatal("blank key should not be secret")
	}
	if !looksSecretKey("webhook_secret") {
		t.Fatal("webhook secret should be secret-like")
	}
	if looksBenignExplicitKey(" ") {
		t.Fatal("blank benign key should be false")
	}
	if looksBenignExplicitKey("bad-key") {
		t.Fatal("bad-key should not be benign")
	}
	if !looksBenignExplicitKey("GOOD_key_1") {
		t.Fatal("GOOD_key_1 should be benign")
	}
}
