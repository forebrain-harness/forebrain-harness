// What forebrain.yaml may carry (plan 207 §3.6, owner ruling 191 §8.25).
package config

import (
	"errors"
	"os"

	"go.yaml.in/yaml/v2"
)

// FileRoot is the whole of forebrain.yaml: the two startup sections. The
// gateway binds before a state database may exist, so its address and token
// live in the file; the credentials pointer stays a file concern because it
// names where a login file lives.
//
// Everything else is stored in fb_config and read from there. The file is
// parsed into this type rather than into Root, so a key the file still carries
// is ignored — the way the owner ruling says a non-supported key must be —
// instead of leaking into the runtime configuration, and so a stored section
// written with the wrong type in the file cannot fail the startup.
type FileRoot struct {
	Gateway     Gateway            `yaml:"gateway,omitempty" json:"gateway,omitempty"`
	Credentials CredentialsSection `yaml:"credentials,omitempty" json:"credentials,omitempty"`
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
	}
}

// LoadLegacyFileSections reads the runtime sections a file-era forebrain.yaml
// still carries, for the one-time import (plan 207 §3.5, §3.6). It is the one
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
