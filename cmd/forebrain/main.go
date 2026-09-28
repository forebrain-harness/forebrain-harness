package main

import (
	"os"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

func main() {
	if exitCode, handled := safety.RunInternalNetworkBridge(); handled {
		os.Exit(exitCode)
	}
	Execute()
}
