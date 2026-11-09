package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

// migrateLegacyHome is process.MigrateLegacyHome for this home, a variable only
// so a test can drive the gate without a home of its own.
var migrateLegacyHome = func(ctx context.Context) error {
	root, err := home.Root()
	if err != nil {
		return err
	}
	cfgPath, err := home.ResolveConfigPath(root)
	if err != nil {
		// No configuration file yet: the first-setup gate is the answer.
		return nil
	}
	if err := process.MigrateLegacyHome(ctx, root, cfgPath); err != nil {
		return err
	}
	process.ResetResolve()
	return nil
}

var gatewayIsTerminal = func(f *os.File) bool {
	return f != nil && term.IsTerminal(int(f.Fd()))
}
var gatewayRunBlocking = gateway.RunServeBlocking

func runGatewayE(cmd *cobra.Command, _ []string) error {
	ctx := context.Background()
	if err := ensureGatewayStartSetup(ctx, cmd); err != nil {
		return err
	}
	// The console log is what an operator reads live and what an operator
	// reads later, so it also lands in <home>/logs with debug.log's rotation.
	root, err := home.Root()
	if err != nil {
		return err
	}
	restoreLog := installGatewayConsoleLog(root)
	defer restoreLog()
	// The sign-in link carries the gateway token, so it is printed only when
	// stdout is a terminal a human is reading — never into captured logs.
	return gatewayRunBlocking(ctx, gateway.ServeOptions{
		Out:        cmd.OutOrStdout(),
		Version:    home.Version,
		SignInLink: gatewayIsTerminal(os.Stdout),
	})
}

func ensureGatewayStartSetup(ctx context.Context, cmd *cobra.Command) error {
	// A pre-plan-268 home keeps what it needs in its .env until the migrations
	// run, and the gates below judge the configuration before anything opens
	// one. Migrating here is what lets such a home start the process that
	// retires the file at all (plan 268).
	if err := migrateLegacyHome(ctx); err != nil {
		return err
	}
	needs, err := gatewayNeedsFirstSetup()
	if err != nil {
		return err
	}
	if needs {
		if !gatewayIsTerminal(os.Stdin) || !gatewayIsTerminal(os.Stdout) {
			// Nobody is here to run the first-setup wizard, and a gateway with
			// no model is a gateway whose first model is about to be added on
			// the web's models page: refusing to start would make that page
			// unreachable (owner 2026-11-07, plan 268 revision).
			return gatewayAllowsModelToFollow(gatewayStartupConfigError())
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

// gatewayAllowsModelToFollow downgrades "no model is configured" to a warning
// and lets the gateway serve: the first model is added on the web's Settings ›
// Models page, and the headless start has no other way to get one. Everything
// else a startup check reports still fails — a broken configuration file, a
// database that will not open, and any error that is not the missing-model one.
func gatewayAllowsModelToFollow(err error) error {
	if err == nil || !errors.Is(err, tui.ErrStartupConfigIncomplete) {
		return err
	}
	slog.Warn("no model is configured yet; sign in to the web UI and add one under Settings › Models")
	return nil
}

func gatewayCommandIO(cmd *cobra.Command) (io.Reader, io.Writer) {
	if cmd == nil {
		return os.Stdin, os.Stdout
	}
	return cmd.InOrStdin(), cmd.OutOrStdout()
}
