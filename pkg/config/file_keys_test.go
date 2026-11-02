package config

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

// TestFileSupportedKeysCoverFileRoot is the whitelist's guard (plan 207 §3.6):
// the keys the file editor accepts and the keys FileRoot decodes must be the
// same set, so a section added to one is a section added to the other.
func TestFileSupportedKeysCoverFileRoot(t *testing.T) {
	var leaves []string
	collectFileKeys("", reflect.TypeOf(FileRoot{}), &leaves)
	sort.Strings(leaves)

	declared := append([]string(nil), FileSupportedKeys()...)
	sort.Strings(declared)

	if !reflect.DeepEqual(leaves, declared) {
		t.Fatalf("FileRoot decodes %v, FileSupportedKeys names %v", leaves, declared)
	}
}

func collectFileKeys(path string, typ reflect.Type, out *[]string) {
	for typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := strings.Split(field.Tag.Get("yaml"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			next := name
			if path != "" {
				next = path + "." + name
			}
			collectFileKeys(next, field.Type, out)
		}
	default:
		*out = append(*out, path)
	}
}

// TestFileRootIsTheWholeFile pins what forebrain.yaml marshals to: the gateway
// and the credentials pointer, and nothing else. rewriteMinimalConfigFile and
// ensureStartupConfigFile both write this type, so this is the shape every
// entry point that creates the file produces.
func TestFileRootIsTheWholeFile(t *testing.T) {
	body, err := yaml.Marshal(FileRoot{})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(body))
	if text != "{}" {
		t.Fatalf("an empty FileRoot marshals to %q, want an empty mapping", text)
	}

	body, err = yaml.Marshal(FileRoot{
		Gateway:     Gateway{HTTPAddr: "127.0.0.1:6060", Auth: GatewayAuth{Token: "${TOKEN}"}},
		Credentials: CredentialsSection{ChatGPT: "auth.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"http_addr: 127.0.0.1:6060", "token: ${TOKEN}", "chatgpt: auth.json"} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("FileRoot lost %q:\n%s", want, body)
		}
	}
}
