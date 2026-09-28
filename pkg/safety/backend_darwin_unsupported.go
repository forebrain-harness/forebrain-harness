//go:build !darwin

package safety

import (
	"context"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

func runSeatbeltCommand(_ context.Context, _ *appcfg.Root, _ CommandRequest, _ string) (CommandResult, error) {
	return CommandResult{}, &UnavailableError{Reason: "seatbelt_not_supported"}
}
