// installdict installs the word-segmentation dictionaries memory search reads
// into dict/ beside a forebrain binary. Every build that produces a binary someone
// runs installs them this way — make build, the npm platform packages, the
// container image, the test drivers — so what is installed is exactly the list
// the search reads (memory.DictionaryFiles).
//
// Usage, from the repository root:
//
//	go run ./cmd/installdict <directory holding the forebrain binary>
package main

import (
	"fmt"
	"os"

	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: installdict <bin-dir>")
		os.Exit(2)
	}
	if err := memory.InstallDictionary(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "installdict:", err)
		os.Exit(1)
	}
}
