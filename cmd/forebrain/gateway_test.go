package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/gateway"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/tui"
	"github.com/spf13/cobra"
)

func TestRunGatewayEAutoRunsOnboardWhenNeeded(t *testing.T) {
	prevNeeds := gatewayNeedsFirstSetup
	prevOnboard := gatewayRunOnboard
	prevStartupError := gatewayStartupConfigError
	prevTTY := gatewayIsTerminal
	prevRun := gatewayRunBlocking
	t.Cleanup(func() {
		gatewayNeedsFirstSetup = prevNeeds
		gatewayRunOnboard = prevOnboard
		gatewayStartupConfigError = prevStartupError
		gatewayIsTerminal = prevTTY
		gatewayRunBlocking = prevRun
	})

	gatewayNeedsFirstSetup = func() (bool, error) { return true, nil }
	gatewayIsTerminal = func(*os.File) bool { return true }
	onboardCalled := false
	gatewayRunOnboard = func(ctx context.Context, in io.Reader, out io.Writer) error {
		onboardCalled = true
		return nil
	}
	gatewayStartupConfigError = func() error { return nil }
	runCalled := false
	gatewayRunBlocking = func(ctx context.Context, opts gateway.ServeOptions) error {
		runCalled = true
		return nil
	}

	cmd := &cobra.Command{Use: "start"}
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(io.Discard)
	if err := runGatewayE(cmd, nil); err != nil {
		t.Fatalf("runGatewayE error: %v", err)
	}
	if !onboardCalled {
		t.Fatal("expected onboard to run")
	}
	if !runCalled {
		t.Fatal("expected gateway serve to start")
	}
}

func TestRunGatewayEReturnsStartupConfigErrorWhenTTYUnavailable(t *testing.T) {
	prevNeeds := gatewayNeedsFirstSetup
	prevOnboard := gatewayRunOnboard
	prevStartupError := gatewayStartupConfigError
	prevTTY := gatewayIsTerminal
	prevRun := gatewayRunBlocking
	t.Cleanup(func() {
		gatewayNeedsFirstSetup = prevNeeds
		gatewayRunOnboard = prevOnboard
		gatewayStartupConfigError = prevStartupError
		gatewayIsTerminal = prevTTY
		gatewayRunBlocking = prevRun
	})

	gatewayNeedsFirstSetup = func() (bool, error) { return true, nil }
	gatewayIsTerminal = func(*os.File) bool { return false }
	gatewayRunOnboard = func(context.Context, io.Reader, io.Writer) error {
		t.Fatal("onboard should not run without TTY")
		return nil
	}
	gatewayStartupConfigError = func() error {
		return fmt.Errorf("no forebrain config found in /tmp/home; run `forebrain gateway start` in a TTY to create one")
	}
	gatewayRunBlocking = func(context.Context, gateway.ServeOptions) error {
		t.Fatal("gateway serve should not start when setup is still missing")
		return nil
	}

	cmd := &cobra.Command{Use: "start"}
	cmd.SetOut(io.Discard)
	err := runGatewayE(cmd, nil)
	if err == nil {
		t.Fatal("expected startup config error")
	}
	if !strings.Contains(err.Error(), "no forebrain config found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// A headless start with no model configured serves anyway (owner 2026-11-07,
// plan 268 revision): the first model is added on the web's Settings › Models
// page, and refusing to start is what kept that page unreachable.
func TestRunGatewayEStartsWithoutAModelOnNonTTY(t *testing.T) {
	prevNeeds := gatewayNeedsFirstSetup
	prevOnboard := gatewayRunOnboard
	prevStartupError := gatewayStartupConfigError
	prevTTY := gatewayIsTerminal
	prevRun := gatewayRunBlocking
	t.Cleanup(func() {
		gatewayNeedsFirstSetup = prevNeeds
		gatewayRunOnboard = prevOnboard
		gatewayStartupConfigError = prevStartupError
		gatewayIsTerminal = prevTTY
		gatewayRunBlocking = prevRun
	})

	gatewayNeedsFirstSetup = func() (bool, error) { return true, nil }
	gatewayIsTerminal = func(*os.File) bool { return false }
	gatewayRunOnboard = func(context.Context, io.Reader, io.Writer) error {
		t.Fatal("onboard should not run without TTY")
		return nil
	}
	gatewayStartupConfigError = func() error {
		return fmt.Errorf("%w: main agent LLM is not fully configured", tui.ErrStartupConfigIncomplete)
	}
	runCalled := false
	gatewayRunBlocking = func(context.Context, gateway.ServeOptions) error {
		runCalled = true
		return nil
	}

	if err := runGatewayE(&cobra.Command{Use: "start"}, nil); err != nil {
		t.Fatalf("runGatewayE error = %v, want the gateway to serve without a model", err)
	}
	if !runCalled {
		t.Fatal("expected the gateway to start")
	}
}

// Every other startup failure still refuses to start.
func TestRunGatewayERefusesOtherStartupFailuresOnNonTTY(t *testing.T) {
	prevNeeds := gatewayNeedsFirstSetup
	prevStartupError := gatewayStartupConfigError
	prevTTY := gatewayIsTerminal
	prevRun := gatewayRunBlocking
	t.Cleanup(func() {
		gatewayNeedsFirstSetup = prevNeeds
		gatewayStartupConfigError = prevStartupError
		gatewayIsTerminal = prevTTY
		gatewayRunBlocking = prevRun
	})

	gatewayNeedsFirstSetup = func() (bool, error) { return true, nil }
	gatewayIsTerminal = func(*os.File) bool { return false }
	gatewayStartupConfigError = func() error { return fmt.Errorf("open the database: refused") }
	gatewayRunBlocking = func(context.Context, gateway.ServeOptions) error {
		t.Fatal("gateway serve should not start")
		return nil
	}

	err := runGatewayE(&cobra.Command{Use: "start"}, nil)
	if err == nil || !strings.Contains(err.Error(), "open the database") {
		t.Fatalf("error = %v, want the startup failure reported", err)
	}
}

func TestRunGatewayEUsesStartupConfigErrorForIncompleteConfig(t *testing.T) {
	prevNeeds := gatewayNeedsFirstSetup
	prevOnboard := gatewayRunOnboard
	prevStartupError := gatewayStartupConfigError
	prevRun := gatewayRunBlocking
	t.Cleanup(func() {
		gatewayNeedsFirstSetup = prevNeeds
		gatewayRunOnboard = prevOnboard
		gatewayStartupConfigError = prevStartupError
		gatewayRunBlocking = prevRun
	})

	gatewayNeedsFirstSetup = func() (bool, error) { return false, nil }
	gatewayRunOnboard = func(context.Context, io.Reader, io.Writer) error {
		t.Fatal("onboard should not run when config file exists but is incomplete")
		return nil
	}
	gatewayStartupConfigError = func() error { return fmt.Errorf("missing env file: /tmp/home/.env") }
	gatewayRunBlocking = func(context.Context, gateway.ServeOptions) error {
		t.Fatal("gateway serve should not start when config is incomplete")
		return nil
	}

	err := runGatewayE(&cobra.Command{Use: "start"}, nil)
	if err == nil {
		t.Fatal("expected startup config error")
	}
	if !strings.Contains(err.Error(), "/tmp/home/.env") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunGatewayEStopsWhenOnboardCancelled(t *testing.T) {
	prevNeeds := gatewayNeedsFirstSetup
	prevOnboard := gatewayRunOnboard
	prevStartupError := gatewayStartupConfigError
	prevTTY := gatewayIsTerminal
	prevRun := gatewayRunBlocking
	t.Cleanup(func() {
		gatewayNeedsFirstSetup = prevNeeds
		gatewayRunOnboard = prevOnboard
		gatewayStartupConfigError = prevStartupError
		gatewayIsTerminal = prevTTY
		gatewayRunBlocking = prevRun
	})

	gatewayNeedsFirstSetup = func() (bool, error) { return true, nil }
	gatewayIsTerminal = func(*os.File) bool { return true }
	gatewayRunOnboard = func(context.Context, io.Reader, io.Writer) error { return tui.ErrCancelled }
	gatewayStartupConfigError = func() error {
		t.Fatal("startup config check should not run after onboard cancellation")
		return nil
	}
	gatewayRunBlocking = func(context.Context, gateway.ServeOptions) error {
		t.Fatal("gateway serve should not start after onboard cancellation")
		return nil
	}

	cmd := &cobra.Command{Use: "start"}
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(io.Discard)
	err := runGatewayE(cmd, nil)
	if err == nil || err != tui.ErrCancelled {
		t.Fatalf("expected ErrCancelled, got %v", err)
	}
}

func TestRunGatewayPassesStdoutAndVersion(t *testing.T) {
	prevNeeds := gatewayNeedsFirstSetup
	prevStartupError := gatewayStartupConfigError
	prevTTY := gatewayIsTerminal
	prevRun := gatewayRunBlocking
	t.Cleanup(func() {
		gatewayNeedsFirstSetup = prevNeeds
		gatewayStartupConfigError = prevStartupError
		gatewayIsTerminal = prevTTY
		gatewayRunBlocking = prevRun
	})

	gatewayNeedsFirstSetup = func() (bool, error) { return false, nil }
	gatewayStartupConfigError = func() error { return nil }
	gatewayIsTerminal = func(*os.File) bool { return false }

	var got gateway.ServeOptions
	var gotOutIsCmdOut bool
	gatewayRunBlocking = func(_ context.Context, opts gateway.ServeOptions) error {
		got = opts
		return nil
	}

	cmd := &cobra.Command{Use: "start"}
	cmdOut := &strings.Builder{}
	cmd.SetOut(cmdOut)
	if err := runGatewayE(cmd, nil); err != nil {
		t.Fatalf("runGatewayE error: %v", err)
	}
	gotOutIsCmdOut = got.Out == io.Writer(cmdOut)

	if !gotOutIsCmdOut {
		t.Fatal("opts.Out must be the command's stdout writer")
	}
	if got.Version != home.Version {
		t.Fatalf("opts.Version = %q, want %q", got.Version, home.Version)
	}
	if got.PrintToken {
		t.Fatal("opts.PrintToken must be false when no token was generated")
	}
}

// A start that had to generate the gateway's token prints it: the token lives
// only in the process, so the banner is the one place to read it, wherever
// stdout goes. A token the operator configured is never printed, on a terminal
// or off one.
func TestRunGatewayPrintsAGeneratedToken(t *testing.T) {
	prevNeeds := gatewayNeedsFirstSetup
	prevStartupError := gatewayStartupConfigError
	prevTTY := gatewayIsTerminal
	prevRun := gatewayRunBlocking
	prevEnsure := ensureGatewayToken
	t.Cleanup(func() {
		gatewayNeedsFirstSetup = prevNeeds
		gatewayStartupConfigError = prevStartupError
		gatewayIsTerminal = prevTTY
		gatewayRunBlocking = prevRun
		ensureGatewayToken = prevEnsure
	})
	gatewayNeedsFirstSetup = func() (bool, error) { return false, nil }
	gatewayStartupConfigError = func() error { return nil }
	gatewayIsTerminal = func(*os.File) bool { return false }

	var got gateway.ServeOptions
	ran := 0
	gatewayRunBlocking = func(_ context.Context, opts gateway.ServeOptions) error {
		ran++
		got = opts
		return nil
	}
	start := func() error {
		cmd := &cobra.Command{Use: "start"}
		cmd.SetOut(io.Discard)
		return runGatewayE(cmd, nil)
	}

	ensureGatewayToken = func(context.Context) (string, error) { return "generated-token", nil }
	if err := start(); err != nil {
		t.Fatalf("runGatewayE error: %v", err)
	}
	if !got.PrintToken {
		t.Fatal("a generated token must be printed even when stdout is not a terminal")
	}
	if len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "gateway.auth.token") {
		t.Fatalf("notes = %q, want one naming the key to set", got.Notes)
	}
	if strings.Contains(got.Notes[0], "generated-token") {
		t.Fatalf("the note must not repeat the token: %q", got.Notes[0])
	}

	// A configured token is nothing to report and is not printed — not even
	// on a terminal.
	ensureGatewayToken = func(context.Context) (string, error) { return "", nil }
	for _, tty := range []bool{false, true} {
		gatewayIsTerminal = func(*os.File) bool { return tty }
		if err := start(); err != nil {
			t.Fatalf("runGatewayE error: %v", err)
		}
		if got.PrintToken || len(got.Notes) != 0 {
			t.Fatalf("terminal=%v: PrintToken = %v, notes = %q; want neither for a configured token", tty, got.PrintToken, got.Notes)
		}
	}
	gatewayIsTerminal = func(*os.File) bool { return false }

	// A start that cannot settle its token does not serve.
	ran = 0
	ensureGatewayToken = func(context.Context) (string, error) { return "", fmt.Errorf("config unreadable") }
	if err := start(); err == nil || !strings.Contains(err.Error(), "config unreadable") {
		t.Fatalf("runGatewayE error = %v, want the failure", err)
	}
	if ran != 0 {
		t.Fatal("the gateway must not start without a token")
	}
}
