package home

import (
	"os"
	"sort"
	"strings"
)

type Options struct {
	ExplicitEnv    map[string]string
	AllowSecretEnv bool
}

var defaultAllow = []string{
	"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "TZ", "USER", "SHELL", "TERM",
}

var explicitRuntimeAllow = map[string]struct{}{
	"FOREBRAIN_HOME":           {},
	"FOREBRAIN_RUN_ID":         {},
	"FOREBRAIN_SESSION_ID":     {},
	"FOREBRAIN_WORKSPACE_ROOT": {},
	"FOREBRAIN_PROJECT_KEY":    {},
	"SAFE_VISIBLE":             {},
}

func SafeSubprocessEnv(parent []string, opts Options) []string {
	if len(parent) == 0 {
		parent = os.Environ()
	}
	allowSet := make(map[string]struct{}, len(defaultAllow))
	for _, k := range defaultAllow {
		allowSet[k] = struct{}{}
	}
	out := make([]string, 0, len(parent)+len(opts.ExplicitEnv))
	for _, item := range parent {
		key, val, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := allowSet[key]; !ok {
			continue
		}
		if !opts.AllowSecretEnv && looksSecretKey(key) {
			continue
		}
		if strings.TrimSpace(val) == "" {
			continue
		}
		out = append(out, key+"="+val)
	}
	for key, val := range opts.ExplicitEnv {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		if !opts.AllowSecretEnv && looksSecretKey(k) {
			continue
		}
		if !opts.AllowSecretEnv {
			if _, ok := explicitRuntimeAllow[strings.ToUpper(k)]; !ok && !looksBenignExplicitKey(k) {
				continue
			}
		}
		out = append(out, k+"="+val)
	}
	sort.Strings(out)
	return out
}

func looksSecretKey(key string) bool {
	k := strings.ToUpper(strings.TrimSpace(key))
	if k == "" {
		return false
	}
	for _, frag := range []string{
		"API_KEY", "TOKEN", "PASSWORD", "SECRET", "PRIVATE_KEY", "SIGNING_KEY", "WEBHOOK_SECRET",
		"ANTHROPIC_", "OPENAI_", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"GOOGLE_APPLICATION_CREDENTIALS", "AZURE_CLIENT_SECRET", "ACTIONS_ID_TOKEN_REQUEST",
		"ACTIONS_RUNTIME_TOKEN", "FOREBRAIN_GATEWAY_TOKEN",
	} {
		if strings.Contains(k, frag) {
			return true
		}
	}
	return false
}

func looksBenignExplicitKey(key string) bool {
	k := strings.TrimSpace(key)
	if k == "" {
		return false
	}
	for _, ch := range k {
		switch {
		case ch >= 'A' && ch <= 'Z':
		case ch >= 'a' && ch <= 'z':
		case ch >= '0' && ch <= '9':
		case ch == '_':
		default:
			return false
		}
	}
	return true
}
