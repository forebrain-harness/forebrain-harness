// forebrain lsp: manage language servers without opening a session — see
// what exists, check why one does not work, flip the switches and install
// binaries.
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/lsp"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/spf13/cobra"
)

var lspCmd = &cobra.Command{
	Use:   "lsp",
	Short: "Manage language servers (code intelligence)",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		return fmt.Errorf("forebrain lsp needs a subcommand: list, doctor, enable, disable, install")
	},
}

var (
	lspListCmd = &cobra.Command{
		Use:   "list",
		Short: "List language servers, their switch and their binary",
		Args:  cobra.NoArgs,
		RunE:  runLSPList,
	}
	lspDoctorCmd = &cobra.Command{
		Use:   "doctor",
		Short: "Check language servers and print what to do about problems",
		Args:  cobra.NoArgs,
		RunE:  runLSPDoctor,
	}
	lspEnableCmd = &cobra.Command{
		Use:   "enable <id>",
		Short: "Enable a language server in trusted projects",
		Args:  cobra.ExactArgs(1),
		RunE:  runLSPEnable,
	}
	lspDisableCmd = &cobra.Command{
		Use:   "disable <id>",
		Short: "Disable a language server",
		Args:  cobra.ExactArgs(1),
		RunE:  runLSPDisable,
	}
	lspInstallCmd = &cobra.Command{
		Use:   "install <id>",
		Short: "Install a language server binary (runs on your machine)",
		Args:  cobra.ExactArgs(1),
		RunE:  runLSPInstall,
	}
	lspRecommendationsCmd = &cobra.Command{
		Use:   "recommendations",
		Short: "Manage language-server recommendations",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("forebrain lsp recommendations needs a subcommand: reset")
		},
	}
	lspRecommendationsResetCmd = &cobra.Command{
		Use:   "reset",
		Short: "Turn language-server recommendations back on",
		Args:  cobra.NoArgs,
		RunE:  runLSPRecommendationsReset,
	}
)

func init() {
	lspListCmd.Flags().String("project", "", "project directory (default: the current directory)")
	lspDoctorCmd.Flags().String("server", "", "check only this server")
	lspDoctorCmd.Flags().String("project", "", "project directory (default: the current directory)")
	lspDoctorCmd.Flags().Bool("no-handshake", false, "skip starting each server once")
	lspInstallCmd.Flags().Bool("yes", false, "run the install command without a prompt")
	lspRecommendationsCmd.AddCommand(lspRecommendationsResetCmd)
	lspCmd.AddCommand(lspListCmd, lspDoctorCmd, lspEnableCmd, lspDisableCmd, lspInstallCmd, lspRecommendationsCmd)
	rootCmd.AddCommand(lspCmd)
}

// lspEnv is what every lsp subcommand needs.
type lspEnv struct {
	home, workspace, projectRoot string
	trusted                      bool
	cfg                          *appcfg.Root
}

// loadLSPEnv resolves the runtime context and the project a subcommand acts
// on. A trust-lookup failure is not an error: like mcp_consent.go, the
// subcommands fail closed — no project, no trust, nothing starts.
func loadLSPEnv(projectDir string) (lspEnv, error) {
	rt, err := process.Resolve()
	if err != nil {
		return lspEnv{}, err
	}
	workspace, err := process.ActiveAgentWorkspace()
	if err != nil {
		return lspEnv{}, err
	}
	if projectDir == "" {
		if projectDir, err = os.Getwd(); err != nil {
			return lspEnv{}, err
		}
	}
	env := lspEnv{home: rt.Home, workspace: workspace, cfg: &rt.Config}
	launch, err := safety.ResolveProjectContext(rt.Home, projectDir)
	if err != nil {
		return env, nil
	}
	env.projectRoot = launch.Project.Root
	env.trusted = safety.TrustedRoot(launch) != ""
	return env, nil
}

// lspProjectFlag reads a subcommand's --project flag.
func lspProjectFlag(cmd *cobra.Command) string {
	dir, _ := cmd.Flags().GetString("project")
	return strings.TrimSpace(dir)
}

// resolvedServers answers the merged server set for env.
func resolvedServers(env lspEnv) []lsp.ServerConfig {
	enabled, err := lsp.LoadEnabled(env.workspace)
	if err != nil {
		enabled = nil // unreadable: resolve as if empty
	}
	return lsp.ResolveServers(lsp.ResolveInput{Config: env.cfg, Enabled: enabled, GOOS: runtime.GOOS})
}

// findServer answers one server of env's merged set, with the CLI's unknown-id
// error text.
func findServer(env lspEnv, id string) (lsp.ServerConfig, error) {
	for _, sc := range resolvedServers(env) {
		if sc.ID == id {
			return sc, nil
		}
	}
	return lsp.ServerConfig{}, fmt.Errorf("unknown language server %q; run forebrain lsp list", id)
}

// serverEnv is the environment a server's binary detection runs with.
func serverEnv(env lspEnv, sc lsp.ServerConfig) []string {
	built, _ := lsp.BuildEnv(lsp.EnvSpec{
		Passthrough: sc.EnvPassthrough,
		Env:         sc.Env,
		FromProject: sc.EnvFromProject,
		Home:        env.home,
	})
	return built
}

// detectCachePath is the agent workspace's detect.json.
func detectCachePath(env lspEnv) string {
	return filepath.Join(lsp.StateDir(env.workspace), "detect.json")
}

func runLSPList(cmd *cobra.Command, _ []string) error {
	env, err := loadLSPEnv(lspProjectFlag(cmd))
	if err != nil {
		return err
	}
	servers := resolvedServers(env)
	// Enabled first, then by id (the snapshot's order).
	sort.SliceStable(servers, func(a, b int) bool {
		if servers[a].Enabled != servers[b].Enabled {
			return servers[a].Enabled
		}
		return servers[a].ID < servers[b].ID
	})

	// Binary detection, at most four at a time; detect.json caches, so the
	// second run is fast.
	detected := make([]lsp.DetectResult, len(servers))
	var (
		wg  sync.WaitGroup
		sem = make(chan struct{}, 4)
	)
	for i := range servers {
		wg.Add(1)
		go func(i int, sc lsp.ServerConfig) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			detected[i] = lsp.Detect(cmd.Context(), sc, serverEnv(env, sc), runtime.GOOS, detectCachePath(env))
		}(i, servers[i])
	}
	wg.Wait()

	out := cmd.OutOrStdout()
	w := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tLANGUAGES\tENABLED\tBINARY")
	for i, sc := range servers {
		enabled := "no"
		if sc.Enabled {
			enabled = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", sc.ID, strings.Join(sc.Languages, ", "), enabled, lspBinaryColumn(sc, detected[i]))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out)
	switch {
	case env.projectRoot == "":
		fmt.Fprintln(out, "Not inside a project: language servers start only in trusted projects.")
	case !env.trusted:
		fmt.Fprintln(out, "This project is not trusted: language servers do not start here.")
	}
	return nil
}

// lspBinaryColumn is the BINARY cell of forebrain lsp list.
func lspBinaryColumn(sc lsp.ServerConfig, det lsp.DetectResult) string {
	switch {
	case sc.Invalid != "":
		return "invalid: " + sc.Invalid
	case det.Installed:
		if det.Version != "" {
			return det.Path + " (" + det.Version + ")"
		}
		return det.Path
	case det.InstallCommand != "":
		return "not installed · forebrain lsp install " + sc.ID
	default:
		return "not installed · no install recipe on this system"
	}
}

func runLSPDoctor(cmd *cobra.Command, _ []string) error {
	env, err := loadLSPEnv(lspProjectFlag(cmd))
	if err != nil {
		return err
	}
	serverID, _ := cmd.Flags().GetString("server")
	serverID = strings.TrimSpace(serverID)
	noHandshake, _ := cmd.Flags().GetBool("no-handshake")
	reports, err := lsp.Doctor(cmd.Context(), lsp.DoctorInput{
		Config:         env.cfg,
		Home:           env.home,
		AgentWorkspace: env.workspace,
		ProjectRoot:    env.projectRoot,
		Trusted:        env.trusted,
		ServerID:       serverID,
		Handshake:      !noHandshake,
	})
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(reports) == 0 {
		fmt.Fprintln(out, "No language server is enabled. Run forebrain lsp list to see the available ones.")
		return nil
	}
	failed := 0
	for i, rep := range reports {
		if i > 0 {
			fmt.Fprintln(out)
		}
		state := "disabled"
		if rep.Enabled {
			state = "enabled"
		}
		fmt.Fprintf(out, "%s (%s) · %s\n", rep.ID, strings.Join(rep.Languages, ", "), state)
		for _, check := range rep.Checks {
			fmt.Fprintf(out, "  %-5s %-11s %s\n", check.Status, check.Name, check.Detail)
			if check.Status == "fail" {
				failed++
			}
		}
		if rep.LogPath != "" {
			fmt.Fprintf(out, "  %-5s %-11s %s\n", "", "log", rep.LogPath)
		}
	}
	if failed > 0 {
		return fmt.Errorf("doctor found %d failing check(s)", failed)
	}
	return nil
}

func runLSPEnable(cmd *cobra.Command, args []string) error {
	env, err := loadLSPEnv("")
	if err != nil {
		return err
	}
	id := strings.TrimSpace(args[0])
	if _, err := findServer(env, id); err != nil {
		return err
	}
	if err := lsp.SaveEnabled(env.workspace, id, true); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Enabled %s. Edits in trusted projects get its diagnostics from the next edit on.\n", id)
	fmt.Fprintln(out, "The lsp tool appears in sessions started from now on.")
	return nil
}

func runLSPDisable(cmd *cobra.Command, args []string) error {
	env, err := loadLSPEnv("")
	if err != nil {
		return err
	}
	id := strings.TrimSpace(args[0])
	if _, err := findServer(env, id); err != nil {
		return err
	}
	if err := lsp.SaveEnabled(env.workspace, id, false); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Disabled %s. Running sessions stop using it from their next edit; instances already running exit when idle.\n", id)
	return nil
}

func runLSPInstall(cmd *cobra.Command, args []string) error {
	env, err := loadLSPEnv("")
	if err != nil {
		return err
	}
	id := strings.TrimSpace(args[0])
	srv, err := findServer(env, id)
	if err != nil {
		return err
	}
	recipe := lsp.UsableInstallRecipe(srv, os.Environ(), runtime.GOOS)
	if recipe == nil {
		// Answers the appendix C install-unavailable text.
		return lsp.Install(context.Background(), srv, runtime.GOOS, "", nil)
	}
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "This runs on your machine, outside the sandbox:")
	fmt.Fprintf(out, "  %s\n", strings.Join(recipe.Argv, " "))
	yes, _ := cmd.Flags().GetBool("yes")
	if !yes {
		if !rootIsTerminal(os.Stdin) {
			return fmt.Errorf("pass --yes to install without a prompt")
		}
		fmt.Fprint(out, "Run it? [y/N] ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			fmt.Fprintln(out, "Cancelled.")
			return nil
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			fmt.Fprintln(out, "Cancelled.")
			return nil
		}
	}
	if err := lsp.Install(cmd.Context(), srv, runtime.GOOS, detectCachePath(env), func(line string) {
		fmt.Fprintf(out, "  %s\n", line)
	}); err != nil {
		return err
	}
	fmt.Fprintf(out, "Installed %s.\n", id)
	enabled, _ := lsp.LoadEnabled(env.workspace)
	if !enabled[id] {
		fmt.Fprintf(out, "Enable it with: forebrain lsp enable %s\n", id)
	}
	return nil
}

func runLSPRecommendationsReset(cmd *cobra.Command, _ []string) error {
	env, err := loadLSPEnv("")
	if err != nil {
		return err
	}
	if err := lsp.ResetRecommendationState(env.workspace); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), `Language server recommendations are back on; servers marked "never" can be suggested again.`)
	return nil
}
