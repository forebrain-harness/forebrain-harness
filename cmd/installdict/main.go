// installdict installs the word-segmentation dictionaries memory search reads
// into dict/ beside a forebrain binary, and verifies that a packaged binary
// carries them. Every build that produces a binary someone runs installs them
// this way — make build, the npm platform packages, the container image, the
// test drivers — so what is installed is exactly the list the search reads
// (memory.DictionaryFiles). -verify is the machine check the packaging step runs
// so a package that cannot search memory is rejected at build time.
//
// Usage, from the repository root:
//
//	go run ./cmd/installdict <directory holding the forebrain binary>
//	go run ./cmd/installdict -verify <directory holding the forebrain binary>
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
)

func main() {
	args := os.Args[1:]
	if len(args) == 2 && args[0] == "-verify" {
		if err := verifyDictionaries(args[1]); err != nil {
			fmt.Fprintln(os.Stderr, "installdict -verify:", err)
			os.Exit(1)
		}
		return
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: installdict <bin-dir>")
		fmt.Fprintln(os.Stderr, "       installdict -verify <bin-dir>")
		os.Exit(2)
	}
	if err := memory.InstallDictionary(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "installdict:", err)
		os.Exit(1)
	}
}

// verifyDictionaries checks that dict/ beside a built binary holds every
// dictionary file this build declares, byte for byte. It reuses the same
// checksum rule a search reads by, so the two can never disagree.
func verifyDictionaries(binDir string) error {
	dictDir := filepath.Join(binDir, memory.DictionaryDir)
	for _, file := range memory.DictionaryFiles {
		path := filepath.Join(dictDir, filepath.FromSlash(file.Name))
		if err := memory.CheckDictionaryFile(path, file.Name); err != nil {
			return err
		}
	}
	return nil
}
