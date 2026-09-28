package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var resumeCmd = &cobra.Command{
	Use:   "resume <session id>",
	Short: "Resume an existing chat session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sessionID := strings.TrimSpace(args[0])
		if sessionID == "" {
			return fmt.Errorf("session id required")
		}
		return runStreamingTerminalWithInitialSessionID(cmd, sessionID)
	},
}

func init() {
	rootCmd.AddCommand(resumeCmd)
}
