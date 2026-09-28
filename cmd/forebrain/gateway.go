package main

import (
	"context"
	"fmt"

	"github.com/forebrain-harness/forebrain-harness/pkg/gateway"
	"github.com/spf13/cobra"
)

func init() {
	gatewayCmd.AddCommand(gatewayStartCmd, gatewayStatusCmd, gatewayStopCmd)
	rootCmd.AddCommand(gatewayCmd)
}

var gatewayCmd = &cobra.Command{
	Use:   "gateway",
	Short: "Manage the gateway service",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		return fmt.Errorf("`forebrain gateway` requires one of: start, status, stop")
	},
}

var gatewayStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start gateway server",
	Args:  cobra.NoArgs,
	RunE:  runGatewayE,
}

var gatewayStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check gateway health endpoint",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return gateway.GatewayHealthText(cmd.Context(), cmd.OutOrStdout())
	},
}

var gatewayStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Request gateway graceful shutdown endpoint",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return gateway.GatewayShutdownRequest(context.Background(), cmd.OutOrStdout())
	},
}
