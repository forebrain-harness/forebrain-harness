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
	if got.SignInLink {
		t.Fatal("opts.SignInLink must be false when stdout is not a terminal")
	}
}
