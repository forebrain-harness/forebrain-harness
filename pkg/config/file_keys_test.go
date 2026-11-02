package config

import (
	"os"
	"path/filepath"
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

// TestFileDatabaseURL pins the one thing the file still decides about the
// state database (plan 209): where it is. The reader is what the three open
// paths call before any database exists, so it must answer from the file's own
// database section — and answer "" only when the file names nothing.
func TestFileDatabaseURL(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	yamlPath := write("forebrain.yaml", "gateway:\n  http_addr: 127.0.0.1:6060\ndatabase:\n  url: postgres://fb:s3cret@pg.corp:5432/forebrain?sslmode=disable\n")
	if got, err := FileDatabaseURL(yamlPath); err != nil || got != "postgres://fb:s3cret@pg.corp:5432/forebrain?sslmode=disable" {
		t.Fatalf("FileDatabaseURL(yaml) = %q, %v", got, err)
	}
	jsonPath := write("forebrain.json", `{"database":{"url":"mysql://fb:pw@mysql.corp:3306/forebrain"}}`)
	if got, err := FileDatabaseURL(jsonPath); err != nil || got != "mysql://fb:pw@mysql.corp:3306/forebrain" {
		t.Fatalf("FileDatabaseURL(json) = %q, %v", got, err)
	}
	// No database section, or a blank url: SQLite, the default every install
	// that never configured one is in.
	plain := write("plain.yaml", "gateway:\n  http_addr: 127.0.0.1:6060\n")
	if got, err := FileDatabaseURL(plain); err != nil || got != "" {
		t.Fatalf("FileDatabaseURL(no database section) = %q, %v", got, err)
	}
	blank := write("blank.yaml", "database:\n  url: \"   \"\n")
	if got, err := FileDatabaseURL(blank); err != nil || got != "" {
		t.Fatalf("FileDatabaseURL(blank url) = %q, %v", got, err)
	}
	// A home with no file at all is the default too, not an error.
	if got, err := FileDatabaseURL(filepath.Join(dir, "missing.yaml")); err != nil || got != "" {
		t.Fatalf("FileDatabaseURL(missing file) = %q, %v", got, err)
	}
	// A file that exists but cannot be parsed is an error: answering "SQLite"
	// for a file that says postgres would open the wrong database.
	broken := write("broken.yaml", "database:\n\turl: [oops\n")
	if _, err := FileDatabaseURL(broken); err == nil {
		t.Fatal("a malformed configuration file read as no database url")
	}
}
