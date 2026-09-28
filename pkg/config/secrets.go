// Secret scanning and redaction.
package config

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v2"
)

func rejectPlaintextConfigSecrets(path string, b []byte) error {
	var doc interface{}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil
	}
	return scanPlaintextConfigSecrets(path, nil, doc)
}

// RejectPlaintextConfigSecrets applies the plaintext-secret scan Load runs on
// forebrain.yaml to a project-level MCP file: a repository must not carry readable
// credentials either. path names the file in the returned error. A body that is
// not valid YAML/JSON passes silently — parse failures are the loader's to
// report, and this scan must never turn a syntax problem into a secret alert.
func RejectPlaintextConfigSecrets(path string, b []byte) error {
	return rejectPlaintextConfigSecrets(path, b)
}

func scanPlaintextConfigSecrets(path string, keyPath []string, v interface{}) error {
	switch x := v.(type) {
	case map[interface{}]interface{}:
		for rawKey, rawValue := range x {
			key := fmt.Sprint(rawKey)
			nextPath := appendKeyPath(keyPath, key)
			if isSecretLikeConfigKey(key) {
				if !isAllowedConfigSecretValue(rawValue) {
					return fmt.Errorf(
						"%s: plaintext secret is not allowed at %s; use ${ENV_NAME} or ~/.forebrain/.env",
						path,
						strings.Join(nextPath, "."),
					)
				}
				continue
			}
			if err := scanPlaintextConfigSecrets(path, nextPath, rawValue); err != nil {
				return err
			}
		}
	case []interface{}:
		for i, item := range x {
			if err := scanPlaintextConfigSecrets(path, appendKeyPath(keyPath, fmt.Sprintf("[%d]", i)), item); err != nil {
				return err
			}
		}
	}
	return nil
}

func appendKeyPath(base []string, key string) []string {
	out := make([]string, 0, len(base)+1)
	out = append(out, base...)
	out = append(out, key)
	return out
}

func isSecretLikeConfigKey(key string) bool {
	k := strings.ToLower(strings.TrimSpace(key))
	k = strings.ReplaceAll(k, "-", "_")
	if isNonSecretConfigKey(k) {
		return false
	}
	switch k {
	case "api_key", "token", "password", "secret", "client_secret", "private_key", "signing_key", "webhook_secret":
		return true
	}
	for _, suffix := range []string{
		"_api_key",
		"_token",
		"_password",
		"_secret",
		"_client_secret",
		"_private_key",
		"_signing_key",
		"_webhook_secret",
	} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

func isNonSecretConfigKey(k string) bool {
	switch k {
	case "insecure_allow_empty_token",
		"token_type",
		"token_url",
		"token_estimate",
		"max_tokens",
		"input_tokens",
		"output_tokens":
		return true
	default:
		return false
	}
}

func isAllowedConfigSecretValue(v interface{}) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	s = strings.TrimSpace(s)
	return s == "" || isSingleEnvReference(s)
}

func isSingleEnvReference(s string) bool {
	if !strings.HasPrefix(s, "${") || !strings.HasSuffix(s, "}") {
		return false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(s, "${"), "}")
	if name == "" {
		return false
	}
	for i, r := range name {
		isAlpha := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
		isDigitAfterFirst := i > 0 && r >= '0' && r <= '9'
		if r == '_' || isAlpha || isDigitAfterFirst {
			continue
		}
		return false
	}
	return true
}

var redactYAMLSecrets = regexp.MustCompile(`(?im)^(\s*(?:[\w.-]*\.)?(?:token|secret|api_key|bot_token|corp_secret|app_secret|client_secret|encoding_aes_key)\s*:\s*)\S.*$`)

func RedactConfigText(s string) string {
	return redactYAMLSecrets.ReplaceAllString(s, "$1[REDACTED]")
}

// RedactedSecretPlaceholder is what a surface sees in place of a secret it is
// not allowed to read back. It doubles as the token a surface sends back when
// it is editing everything *except* that secret: ClearRedactedSecrets turns it
// into an empty string, and Save then restores the value already on disk.
const RedactedSecretPlaceholder = "[REDACTED]"

// RedactSecrets replaces every set secret-like string in the value with the
// placeholder, in place. Pass a pointer to the struct being sent to a client.
//
// An empty field stays empty: "unset" and "set but hidden" are different facts,
// and a settings screen has to be able to tell them apart.
func RedactSecrets(v any) {
	walkSecretStrings(reflect.ValueOf(v), func(field reflect.Value) {
		if field.String() != "" {
			field.SetString(RedactedSecretPlaceholder)
		}
	})
}

// ClearRedactedSecrets blanks every secret-like string that still carries the
// placeholder, so a round-tripped settings form means "leave that one alone".
func ClearRedactedSecrets(v any) {
	walkSecretStrings(reflect.ValueOf(v), func(field reflect.Value) {
		if field.String() == RedactedSecretPlaceholder {
			field.SetString("")
		}
	})
}

func walkSecretStrings(v reflect.Value, apply func(reflect.Value)) {
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface:
		if v.IsNil() {
			return
		}
		walkSecretStrings(v.Elem(), apply)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			field := v.Type().Field(i)
			if field.PkgPath != "" {
				continue
			}
			value := v.Field(i)
			name := strings.Split(field.Tag.Get("yaml"), ",")[0]
			if name == "" {
				name = strings.Split(field.Tag.Get("json"), ",")[0]
			}
			if value.Kind() == reflect.String && isSecretLikeConfigKey(name) {
				if value.CanSet() {
					apply(value)
				}
				continue
			}
			walkSecretStrings(value, apply)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			walkSecretStrings(v.Index(i), apply)
		}
	case reflect.Map:
		// Map values are not addressable, so each one is copied out, walked,
		// and written back.
		for _, key := range v.MapKeys() {
			item := v.MapIndex(key)
			copied := reflect.New(item.Type()).Elem()
			copied.Set(item)
			walkSecretStrings(copied, apply)
			v.SetMapIndex(key, copied)
		}
	}
}
