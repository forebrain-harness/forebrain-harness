//go:build !windows

package safety

import (
	"context"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func windowsSandboxUnavailableReason() string { return "windows_sandbox_wrong_platform" }

func runWindowsSandboxCommand(context.Context, *appcfg.Root, CommandRequest, string) (CommandResult, error) {
	return CommandResult{}, &UnavailableError{Reason: windowsSandboxUnavailableReason()}
}
