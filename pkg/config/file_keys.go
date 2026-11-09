// What forebrain.yaml may carry (plan 207 §3.6, owner ruling 191 §8.25).
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v2"
	yamlv3 "gopkg.in/yaml.v3"
)

// FileRoot is the whole of forebrain.yaml: the three startup sections. The
// gateway binds before a state database may exist, so its address and token
// live in the file; the credentials pointer stays a file concern because it
// names where a login file lives. The database section names where the state
// database itself is, which is the one thing that cannot be stored in it
// (plan 209).
//
// Everything else is stored in fb_config and read from there. The file is
// parsed into this type rather than into Root, so a key the file still carries
// is ignored — the way the owner ruling says a non-supported key must be —
// instead of leaking into the runtime configuration, and so a stored section
// written with the wrong type in the file cannot fail the startup.
type FileRoot struct {
	Gateway     Gateway            `yaml:"gateway,omitempty" json:"gateway,omitempty"`
	Credentials CredentialsSection `yaml:"credentials,omitempty" json:"credentials,omitempty"`
	Database    DatabaseSection    `yaml:"database,omitempty" json:"database,omitempty"`
}

// DatabaseSection is where the state database lives (plan 209, design §2.1).
// An empty URL is the default: the SQLite file under the home's state
// directory. Otherwise the URL's scheme picks the engine — mysql:// for MySQL
// 8.0 or later, postgres:// or postgresql:// for PostgreSQL 14 or later.
type DatabaseSection struct {
	URL string `yaml:"url,omitempty" json:"url,omitempty"`
}

// FileSupportedKeys names every key forebrain.yaml may carry, as the dotted
// path a reader sees in the file. The configuration-file editor uses it to flag
// a key the file does not support, and this list is its single source: a plan
// that gives the file a new section (the multi-database plan's database
// section) adds it here and in FileRoot, and TestFileSupportedKeysCoverFileRoot
// keeps the two agreeing.
func FileSupportedKeys() []string {
	return []string{
		"gateway.http_addr",
		"gateway.auth.token",
		"credentials.chatgpt",
		"database.url",
	}
}

// FileDatabaseURL reads the state database URL the configuration file names,
// or "" when it names none (no file, no database section, no url) — which is
// the SQLite default. It reads only this section, and it reads it before any
// database is open, so it must not depend on Root or the composed
// configuration: the URL is where the database is, not something stored in it
// (plan 209, design §2.1).
//
// An unreadable file is an error rather than an empty URL: answering "SQLite"
// for a file that says "postgres://…" but has a typo would open the wrong
// database and look like data loss.
func FileDatabaseURL(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var file FileRoot
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(b, &file); err != nil {
			return "", err
		}
	case ".json":
		if err := json.Unmarshal(b, &file); err != nil {
			return "", err
		}
	default:
		if err := json.Unmarshal(b, &file); err != nil {
			if yamlErr := yaml.Unmarshal(b, &file); yamlErr != nil {
				return "", err
			}
		}
	}
	return strings.TrimSpace(file.Database.URL), nil
}

// LoadLegacyFileSections reads the runtime sections a file-era forebrain.yaml// still carries, for the one-time import (plan 207 §3.5, §3.6). It is the one
// reader that looks past the supported keys, because moving an old file's
// sections into the state database is what the import is for; every other
// reader — Load, LoadPersisted — sees FileRoot.
//
// It deliberately runs neither the plaintext-secret scan nor the loader's
// validation: such a file is exactly what the import retires, and the file is
// rewritten down to its supported keys before anything reads it as the
// runtime's configuration again. A section whose value its own type cannot hold
// is skipped — the rest of the file still imports, and a mistyped old section
// must not cost the install its startup.
func LoadLegacyFileSections(path string) (Root, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Root{}, err
	}
	var r Root
	// yaml.v2 reports a mistyped field as a *TypeError and keeps decoding the
	// others, which is the per-section tolerance this reader wants; a syntax
	// error is the file being unreadable and is reported.
	if err := yaml.Unmarshal(b, &r); err != nil {
		var typeErr *yaml.TypeError
		if !errors.As(err, &typeErr) {
			return Root{}, err
		}
	}
	normalize(&r)
	return r, nil
}

// SetGatewayToken rewrites gateway.auth.token in the configuration file and
// changes nothing else. The gateway's token lives either in the process
// environment (a container supplies it there) or in this file (the home's own
// secret), and the one-time migration of a home that kept it in `.env` writes
// it here as plaintext, in a file only its owner can read (owner 2026-11-07,
// plan 268).
//
// A YAML file is edited as a document, not re-marshalled from Root: rewriting
// from the parsed configuration would drop the comments a hand-edited file
// carries and would rewrite every other key. A JSON file has neither comments
// nor unsupported keys, so it round-trips through FileRoot.
func SetGatewayToken(path, token string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.ToLower(filepath.Ext(path)) == ".json" {
		var file FileRoot
		if err := json.Unmarshal(b, &file); err != nil {
			return err
		}
		file.Gateway.Auth.Token = token
		out, err := json.MarshalIndent(file, "", "  ")
		if err != nil {
			return err
		}
		return writeFileAtomic(path, append(out, '\n'), 0o600)
	}
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(b, &doc); err != nil {
		return err
	}
	if !setMappingScalar(&doc, []string{"gateway", "auth", "token"}, token) {
		return fmt.Errorf("%s: gateway.auth.token is not in the file", path)
	}
	// Two-space indentation, the shape every file this code writes has: the
	// edit must not reindent the rest of a hand-edited file.
	var out bytes.Buffer
	enc := yamlv3.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return writeFileAtomic(path, out.Bytes(), 0o600)
}

// setMappingScalar replaces the scalar at a dotted key path of a YAML document
// and reports whether it found one. The value is written double-quoted so a
// token that happens to read as a number, a bool or an empty string stays the
// string it is.
func setMappingScalar(n *yamlv3.Node, path []string, value string) bool {
	if n == nil || len(path) == 0 {
		return false
	}
	if n.Kind == yamlv3.DocumentNode {
		if len(n.Content) == 0 {
			return false
		}
		return setMappingScalar(n.Content[0], path, value)
	}
	if n.Kind != yamlv3.MappingNode {
		return false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value != path[0] {
			continue
		}
		target := n.Content[i+1]
		if len(path) > 1 {
			return setMappingScalar(target, path[1:], value)
		}
		if target.Kind != yamlv3.ScalarNode {
			return false
		}
		target.Value = value
		target.Tag = "!!str"
		target.Style = yamlv3.DoubleQuotedStyle
		return true
	}
	return false
}
