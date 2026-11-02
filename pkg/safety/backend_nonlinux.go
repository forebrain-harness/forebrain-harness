//go:build !linux

package safety

import (
	"context"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func runBubblewrapCommand(_ context.Context, _ *appcfg.Root, _ CommandRequest, _ string) (CommandResult, error) {
	return CommandResult{}, &UnavailableError{Reason: "bubblewrap_not_supported"}
}

type linuxMissingDenyMarker struct{}
