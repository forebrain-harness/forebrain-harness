package main

import (
	"context"
	"io"
	"os"

	"github.com/forebrain-harness/forebrain-harness/pkg/gateway"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var gatewayNeedsFirstSetup = tui.NeedsFirstSetup
var gatewayRunOnboard = onboardRunner
var gatewayStartupConfigError = tui.StartupConfigError
var gatewayIsTerminal = func(f *os.File) bool {
	return f != nil && term.IsTerminal(int(f.Fd()))
}
var gatewayRunBlocking = gateway.RunServeBlocking

func runGatewayE(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()
	if err := ensureGatewayStartSetup(ctx, cmd); err != nil {
		return err
	}
	// The sign-in link carries the gateway token, so it is printed only when
	// stdout is a terminal a human is reading — never into captured logs.
	return gatewayRunBlocking(ctx, gateway.ServeOptions{
		Out:        cmd.OutOrStdout(),
		Version:    home.Version,
		SignInLink: gatewayIsTerminal(os.Stdout),
	})
}

func ensureGatewayStartSetup(ctx context.Context, cmd *cobra.Command) error {
	needs, err := gatewayNeedsFirstSetup()
	if err != nil {
		return err
	}
	if needs {
		if !gatewayIsTerminal(os.Stdin) || !gatewayIsTerminal(os.Stdout) {
			return gatewayStartupConfigError()
		}
		in, out := gatewayCommandIO(cmd)
		if err := gatewayRunOnboard(ctx, in, out); err != nil {
			return err
		}
		process.ResetResolve()
	}
	if err := gatewayStartupConfigError(); err != nil {
		return err
	}
	return nil
}

func gatewayCommandIO(cmd *cobra.Command) (io.Reader, io.Writer) {
	if cmd == nil {
		return os.Stdin, os.Stdout
	}
	return cmd.InOrStdin(), cmd.OutOrStdout()
}
