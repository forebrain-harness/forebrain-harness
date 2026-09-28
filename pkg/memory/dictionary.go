package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// The word-segmentation dictionaries memory search reads.
//
// They are tens of megabytes of word lists, so they are not compiled into the
// binary: every package that installs forebrain puts them in DictionaryDir beside
// it, and they are read from there. DictionaryFiles is the one list of them —
// what the search reads and what the release installs are the same entries, so
// the two cannot drift apart.

// DictionaryDir is the directory, beside the forebrain binary, that holds the
// dictionaries.
const DictionaryDir = "dict"

// DictionaryFile is one dictionary file: where it lives under DictionaryDir,
// and the Go module, at the version go.mod selects, whose file it is.
type DictionaryFile struct {
	Name   string
	Module string
	Source string
}

const (
	chineseSimplifiedWords  = "zh/s_1.txt"
	chineseTraditionalWords = "zh/t_1.txt"
	chineseStopWords        = "zh/stop_tokens.txt"
	japaneseDictionary      = "ja/ipa.dict"
)

// DictionaryFiles lists every dictionary file: the simplified and traditional
// Chinese word lists and the Chinese stop words, and the Japanese morphological
// dictionary (IPADIC).
var DictionaryFiles = []DictionaryFile{
	{Name: chineseSimplifiedWords, Module: "github.com/go-ego/gse", Source: "data/dict/zh/s_1.txt"},
	{Name: chineseTraditionalWords, Module: "github.com/go-ego/gse", Source: "data/dict/zh/t_1.txt"},
	{Name: chineseStopWords, Module: "github.com/go-ego/gse", Source: "data/dict/zh/stop_tokens.txt"},
	{Name: japaneseDictionary, Module: "github.com/ikawaha/kagome-dict/ipa", Source: "ipa.dict"},
}

// dictionaryPath is where the named dictionary file is read from: under
// DictionaryDir, beside the binary that is running.
func dictionaryPath(name string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(executable), DictionaryDir, filepath.FromSlash(name)), nil
}

// InstallDictionary copies every dictionary file into DictionaryDir under
// binDir, the directory that holds a forebrain binary. It is how the release and
// the test binaries alike get their dictionaries, and it runs the go command in
// the forebrain module, which knows where each dictionary's module is.
func InstallDictionary(binDir string) error {
	dirs := map[string]string{}
	for _, file := range DictionaryFiles {
		dir, known := dirs[file.Module]
		if !known {
			var err error
			if dir, err = moduleDir(file.Module); err != nil {
				return err
			}
			dirs[file.Module] = dir
		}
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file.Source)))
		if err != nil {
			return err
		}
		target := filepath.Join(binDir, DictionaryDir, filepath.FromSlash(file.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// moduleDir downloads module at the version the forebrain module selects and
// returns the directory it was unpacked into.
func moduleDir(module string) (string, error) {
	out, err := exec.Command("go", "mod", "download", "-json", module).Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("go mod download %s: %w: %s", module, err, exit.Stderr)
		}
		return "", fmt.Errorf("go mod download %s: %w", module, err)
	}
	var download struct{ Dir, Error string }
	if err := json.Unmarshal(out, &download); err != nil {
		return "", fmt.Errorf("go mod download %s: %w", module, err)
	}
	if download.Error != "" {
		return "", fmt.Errorf("go mod download %s: %s", module, download.Error)
	}
	return download.Dir, nil
}
