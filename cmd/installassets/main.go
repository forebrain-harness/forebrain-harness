// installassets installs the runtime asset files beside a forebrain binary.
// Every build that produces a binary someone runs installs them this way —
// make build, the npm platform packages, the container image, the test
// drivers — so what is installed is exactly the list the runtime reads
// (llm.assetEntries).
//
// Usage, from the repository root:
//
//	go run ./cmd/installassets <directory holding the forebrain binary>
package main

import (
	"fmt"
	"os"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: installassets <bin-dir>")
		os.Exit(2)
	}
	if err := llm.InstallAssets("assets", os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "installassets:", err)
		os.Exit(1)
	}
}
