package home

import (
	"os"
	"runtime"
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

// mirrorEnvAllow is the package-mirror and certificate environment a child
// process inherits. An intranet deployment has no public network: every command
// there resolves its packages through the mirror the operator configured on the
// gateway, and dropping these names made `go mod download`, `pip install` and
// `npm install` reach for the internet and fail. They are configuration, not
// credentials — a value that carries one (https://user:pass@mirror) is passed
// through as the operator wrote it — and looksSecretKey still filters the list
// below. Proxy variables (HTTP_PROXY, HTTPS_PROXY, ALL_PROXY, NO_PROXY) are
// deliberately absent: an intranet install reaches its mirror directly.
var mirrorEnvAllow = []string{
	// Go modules.
	"GOPROXY", "GONOPROXY", "GOPRIVATE", "GONOSUMDB", "GONOSUMCHECK", "GOSUMDB", "GOINSECURE",
	"GOTOOLCHAIN",
	// pip and uv.
	"PIP_INDEX_URL", "PIP_EXTRA_INDEX_URL", "PIP_TRUSTED_HOST", "PIP_CERT",
	"UV_INDEX_URL", "UV_DEFAULT_INDEX", "UV_EXTRA_INDEX_URL", "UV_INDEX", "UV_INSECURE_HOST",
	// npm, yarn and pnpm. npm itself looks its configuration up
	// case-insensitively, so both spellings of every name are listed.
	"NPM_CONFIG_REGISTRY", "npm_config_registry", "NPM_CONFIG_CAFILE", "npm_config_cafile",
	"NPM_CONFIG_STRICT_SSL", "npm_config_strict_ssl",
	"YARN_REGISTRY", "YARN_NPM_REGISTRY_SERVER",
	// The enterprise CA an internal mirror's HTTPS certificate is signed with.
	"SSL_CERT_FILE", "SSL_CERT_DIR", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "CURL_CA_BUNDLE",
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
	allowSet := make(map[string]struct{}, len(defaultAllow)+len(mirrorEnvAllow))
	for _, k := range defaultAllow {
		allowSet[k] = struct{}{}
	}
	// Names match exactly, and the secret filter below still applies to the
	// merged set: this adds the mirror's configuration, never credentials.
	for _, k := range mirrorEnvAllow {
		allowSet[k] = struct{}{}
	}
	// One entry per variable, and the explicit value replaces the inherited one
	// outright. Appending both and sorting the merged slice — what this used to
	// do — left the winner to os/exec's "last entry for a duplicate key" rule,
	// which a plain sort decides by the spelling of the two values: the task's
	// own TMPDIR lost to the parent's whenever the parent's path happened to
	// sort after it, and the same held for HOME and every mirror variable.
	merged := make(map[string]envValue, len(parent)+len(opts.ExplicitEnv))
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
		merged[envVariableKey(key)] = envValue{name: key, value: val}
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
		merged[envVariableKey(k)] = envValue{name: k, value: val}
	}
	// Sorting is still worth doing — it keeps the output deterministic, and
	// tests and diffs read by variable — but it no longer decides anything: the
	// duplicates the sort used to order are gone by now.
	out := make([]string, 0, len(merged))
	for _, entry := range merged {
		out = append(out, entry.name+"="+entry.value)
	}
	sort.Strings(out)
	return out
}

// envValue is one environment entry, keeping the spelling of the side that
// won: the parent's for an inherited variable, the explicit one's for an
// overridden one.
type envValue struct {
	name  string
	value string
}

// envVariableKey is the identity a variable is merged by. Windows treats
// environment names case-insensitively — `Path` and `PATH` are one variable —
// while everywhere else the spelling is part of the name. The uppercase key is
// never what gets emitted; it only decides which entries are the same entry.
func envVariableKey(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
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
