package main

import (
	"reflect"
	"sort"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/spf13/cobra"
)

func TestRootCommandExposesItsSubcommands(t *testing.T) {
	var names []string
	for _, command := range rootCmd.Commands() {
		if !command.Hidden {
			names = append(names, command.Name())
		}
	}
	sort.Strings(names)
	if want := []string{"gateway", "lsp", "resume"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("visible commands=%v want=%v", names, want)
	}
	var gatewayNames []string
	for _, command := range gatewayCmd.Commands() {
		if !command.Hidden {
			gatewayNames = append(gatewayNames, command.Name())
		}
	}
	sort.Strings(gatewayNames)
	if want := []string{"start", "status", "stop"}; !reflect.DeepEqual(gatewayNames, want) {
		t.Fatalf("gateway subcommands=%v want=%v", gatewayNames, want)
	}
	if err := gatewayCmd.RunE(gatewayCmd, nil); err == nil {
		t.Fatal("forebrain gateway must fail without start/status/stop")
	}
}

func TestRootAndGatewayRejectPositionalArguments(t *testing.T) {
	if err := rootCmd.Args(rootCmd, []string{"status"}); err == nil {
		t.Fatal("root accepted removed command as a positional argument")
	}
	if err := gatewayCmd.Args(gatewayCmd, []string{"unknown"}); err == nil {
		t.Fatal("gateway accepted an unknown positional argument")
	}
}

func TestHelpFlagReplacesHelpSubcommand(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()
	for _, command := range rootCmd.Commands() {
		if command.Name() == "help" {
			t.Fatal("help subcommand must not be registered")
		}
	}
	if err := rootCmd.Args(rootCmd, []string{"help"}); err == nil {
		t.Fatal("help must not be accepted as a root positional argument")
	}
	rootCmd.InitDefaultHelpFlag()
	if rootCmd.Flags().Lookup("help") == nil {
		t.Fatal("--help flag must remain available")
	}
}

func TestExecuteSetsRootCommandVersionFromBuildInfo(t *testing.T) {
	originalVersion := home.Version
	originalRunE := rootCmd.RunE
	home.Version = "v9.9.9-test"
	rootCmd.RunE = func(cmd *cobra.Command, args []string) error { return nil }
	t.Cleanup(func() {
		home.Version = originalVersion
		rootCmd.RunE = originalRunE
		rootCmd.Version = ""
	})

	Execute()

	if got := rootCmd.Version; got != "v9.9.9-test" {
		t.Fatalf("rootCmd.Version=%q want %q", got, "v9.9.9-test")
	}
}
