package home

import (
	"runtime"
	"strings"
	"testing"
)

// envValues collapses a slice into a map and fails on a duplicated name: one
// entry per variable is the invariant the merge owes every caller, because
// os/exec keeps only the last of a duplicated key and that choice was
// previously made by the sort order of the two values.
func envValues(t *testing.T, env []string) map[string]string {
	t.Helper()
	values := make(map[string]string, len(env))
	for _, item := range env {
		key, val, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, dup := values[key]; dup {
			t.Fatalf("%s appears twice in the environment: %#v", key, env)
		}
		values[key] = val
	}
	return values
}

// An explicit value wins wherever the two paths happen to sort, which is the
// case the merge used to lose: the parent's temp directory sorts after the
// task's own, so the sort made the parent's value the last entry and os/exec
// kept it.
func TestSafeSubprocessEnvExplicitTMPDIRBeatsTheParent(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"TMPDIR=/var/folders/zz/T/",
		"TMP=/var/folders/zz/T/",
		"TEMP=/var/folders/zz/T/",
	}
	got := SafeSubprocessEnv(parent, Options{ExplicitEnv: map[string]string{
		"TMPDIR": "/Users/u/work/tasks/1/.work/tmp",
	}})
	values := envValues(t, got)
	if values["TMPDIR"] != "/Users/u/work/tasks/1/.work/tmp" {
		t.Fatalf("TMPDIR = %q, want the explicit value: %#v", values["TMPDIR"], got)
	}
	// The names the explicit env did not mention keep the parent's value.
	if values["TMP"] != "/var/folders/zz/T/" || values["TEMP"] != "/var/folders/zz/T/" {
		t.Fatalf("untouched temp variables = %q/%q: %#v", values["TMP"], values["TEMP"], got)
	}
}

func TestSafeSubprocessEnvExplicitHOMEBeatsTheParent(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"HOME=/Users/u",
		"LANG=en_US.UTF-8",
	}
	got := SafeSubprocessEnv(parent, Options{ExplicitEnv: map[string]string{
		"HOME": "/Users/u/work/tasks/1/.work/home",
	}})
	values := envValues(t, got)
	if values["HOME"] != "/Users/u/work/tasks/1/.work/home" {
		t.Fatalf("HOME = %q, want the explicit value: %#v", values["HOME"], got)
	}
	// A parent-only variable survives the merge.
	if values["LANG"] != "en_US.UTF-8" || values["PATH"] != "/usr/bin" {
		t.Fatalf("inherited variables were dropped: %#v", got)
	}
}

// Windows has one PATH, however it is spelled, so the explicit value replaces
// the inherited `Path` instead of shipping both: os/exec would otherwise hand
// the child two entries and the toolchain would see whichever came last.
func TestSafeSubprocessEnvFoldsCaseOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("environment names are case-insensitive only on Windows")
	}
	parent := []string{
		"Path=C:\\Windows\\system32",
		"HOME=C:\\Users\\u",
	}
	got := SafeSubprocessEnv(parent, Options{ExplicitEnv: map[string]string{
		"PATH": "C:\\tools;C:\\Windows\\system32",
	}})
	values := map[string]string{}
	spellings := map[string]int{}
	for _, item := range got {
		key, val, _ := strings.Cut(item, "=")
		values[strings.ToUpper(key)] = val
		spellings[strings.ToUpper(key)]++
	}
	if spellings["PATH"] != 1 {
		t.Fatalf("PATH reached the child %d time(s), want one entry: %#v", spellings["PATH"], got)
	}
	if values["PATH"] != `C:\tools;C:\Windows\system32` {
		t.Fatalf("PATH = %q, want the explicit value: %#v", values["PATH"], got)
	}
}

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

// An intranet install resolves its packages through the mirror the operator
// configured on the gateway, so those names reach every child process — while
// the proxy and credential names the guard exists for do not.
func TestSafeSubprocessEnvKeepsTheMirrorConfigurationOnly(t *testing.T) {
	parent := []string{
		"PATH=/usr/bin",
		"GOPROXY=http://goproxy.corp",
		"PIP_INDEX_URL=https://pip.corp/simple",
		"npm_config_registry=https://npm.corp",
		"SSL_CERT_FILE=/etc/ssl/corp-ca.pem",
		"HTTPS_PROXY=http://proxy.corp:3128",
		"GITHUB_TOKEN=ghp_secret",
	}
	got := SafeSubprocessEnv(parent, Options{})
	joined := map[string]bool{}
	for _, item := range got {
		joined[item] = true
	}
	for _, want := range []string{
		"GOPROXY=http://goproxy.corp",
		"PIP_INDEX_URL=https://pip.corp/simple",
		"npm_config_registry=https://npm.corp",
		"SSL_CERT_FILE=/etc/ssl/corp-ca.pem",
	} {
		if !joined[want] {
			t.Errorf("mirror variable %q was dropped: %#v", want, got)
		}
	}
	for _, forbidden := range []string{"HTTPS_PROXY=http://proxy.corp:3128", "GITHUB_TOKEN=ghp_secret"} {
		if joined[forbidden] {
			t.Errorf("forbidden env leaked: %s", forbidden)
		}
	}
	// The whitelist stays configuration, never credentials: nothing on it is a
	// name the secret filter would strip.
	for _, name := range mirrorEnvAllow {
		if looksSecretKey(name) {
			t.Errorf("mirrorEnvAllow lists %q, which the secret filter removes", name)
		}
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
