package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/telemetry"
	"github.com/forebrain-harness/forebrain-harness/pkg/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var globalHome string
var globalYOLO bool
var rootIsTerminal = func(f *os.File) bool {
	return f != nil && term.IsTerminal(int(f.Fd()))
}

var rootCmd = &cobra.Command{
	Use:           "forebrain",
	Short:         "Forebrain Harness interactive agent",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          rootMainRun,
}

func Execute() {
	rootCmd.Version = home.Version
	rootCmd.SetVersionTemplate("{{.Version}}\n")
	if err := rootCmd.Execute(); err != nil {
		if errors.Is(err, tui.ErrCancelled) {
			return
		}
		rh := ""
		if root, derr := home.Root(); derr == nil {
			rh = root
		}
		telemetry.ErrorThenFprintln(rh, os.Stderr, err, "cmd/forebrain rootCmd.Execute")
		os.Exit(1)
	}
}

func init() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: defaultSlogLevel()})))
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	// Cobra requires a help-command placeholder when subcommands exist. Keep
	// the --help flag as the only public help entrypoint and hide the placeholder
	// behind a non-public name that always fails if invoked directly.
	rootCmd.SetHelpCommand(&cobra.Command{
		Use:    "__help",
		Hidden: true,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("unknown command %q for %q", "__help", "forebrain")
		},
	})
	rootCmd.PersistentFlags().StringVar(&globalHome, "home", "", "data root directory (sets FOREBRAIN_HOME)")
	rootCmd.PersistentFlags().BoolVar(&globalYOLO, "yolo", false, "bypass tool approvals and force danger-full-access execution for executable tools")
	rootCmd.PersistentPreRunE = prepPersistentHome
}

func defaultSlogLevel() slog.Level {
	for _, env := range []string{"LOG_LEVEL", "FOREBRAIN_LOG_LEVEL"} {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(env))) {
		case "error":
			return slog.LevelError
		case "warn", "warning":
			return slog.LevelWarn
		case "info":
			return slog.LevelInfo
		case "debug":
			return slog.LevelDebug
		}
	}
	return slog.LevelDebug
}

func rootMainRun(cmd *cobra.Command, _ []string) error {
	tty := rootIsTerminal(os.Stdin) && rootIsTerminal(os.Stdout)
	if !tty {
		printNonInteractiveHint(cmd.ErrOrStderr())
		return fmt.Errorf("non-interactive")
	}
	return runInteractive(cmd)
}

func prepPersistentHome(cmd *cobra.Command, args []string) error {
	if globalHome != "" {
		if err := os.Setenv("FOREBRAIN_HOME", globalHome); err != nil {
			return err
		}
	}
	root, err := home.Root()
	if err != nil {
		return err
	}
	if err := os.Setenv("FOREBRAIN_HOME", root); err != nil {
		return err
	}
	if globalYOLO {
		if err := os.Setenv("FOREBRAIN_YOLO", "1"); err != nil {
			return err
		}
	}
	inner := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: defaultSlogLevel(), AddSource: true})
	slog.SetDefault(slog.New(telemetry.NewSlogTeeHandler(root, inner)))
	return nil
}
