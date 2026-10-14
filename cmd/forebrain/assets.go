// forebrain assets: the runtime files memory search and the model catalog read
// from — the word-segmentation dictionaries in particular. It is the product's
// own way to install them, so a machine the packaging scripts never touched can
// still be made to search memory.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/memory"
	"github.com/spf13/cobra"
)

var assetsCmd = &cobra.Command{
	Use:   "assets",
	Short: "Install and inspect the runtime assets memory search and the model catalog read",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		return fmt.Errorf("forebrain assets needs a subcommand: install, status")
	},
}

var (
	assetsInstallCmd = &cobra.Command{
		Use:   "install",
		Short: "Install the memory dictionaries (and models.json) for this home",
		Args:  cobra.NoArgs,
		RunE:  runAssetsInstall,
	}
	assetsStatusCmd = &cobra.Command{
		// Resolving a dictionary seeds the home and may download this version's
		// release bundle, so asking for status is also a repair.
		Use:   "status",
		Short: "Report where each dictionary is read from (also repairs missing ones)",
		Args:  cobra.NoArgs,
		RunE:  runAssetsStatus,
	}
)

func init() {
	assetsInstallCmd.Flags().String("bin-dir", "", "install into <dir>/dict beside a binary instead of the forebrain home")
	assetsCmd.AddCommand(assetsInstallCmd, assetsStatusCmd)
	rootCmd.AddCommand(assetsCmd)
}

// runAssetsInstall places every dictionary this build declares where a search
// reads it: the home by default, or <dir>/dict beside a binary with --bin-dir
// (the build-time placement scripts/install-dictionary.sh does). It refuses to
// report success when a dictionary could not be installed from any source.
func runAssetsInstall(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	binDir, _ := cmd.Flags().GetString("bin-dir")
	if binDir = strings.TrimSpace(binDir); binDir != "" {
		if err := memory.InstallDictionary(binDir); err != nil {
			return err
		}
		fmt.Fprintf(out, "installed memory dictionaries into %s\n", filepath.Join(binDir, memory.DictionaryDir))
		return nil
	}
	root, err := home.Root()
	if err != nil {
		return err
	}
	sourceDir, err := assetsSourceDir()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "installing memory dictionaries into %s from %s\n", filepath.Join(root, memory.DictionaryDir), sourceDir)
	installed, missing := 0, 0
	for _, status := range memory.DictionariesStatus(root, sourceDir) {
		if status.Err != nil {
			missing++
			fmt.Fprintf(out, "%s  missing  %v\n", status.File.Name, status.Err)
			continue
		}
		installed++
		fmt.Fprintf(out, "%s  ok  %s\n", status.File.Name, status.Path)
	}
	// The model catalog travels beside the dictionaries; seed it too, and
	// report it without failing the dictionary install over it.
	if err := llm.Install(root, home.Version); err != nil {
		fmt.Fprintf(out, "models.json  missing  %v\n", err)
	} else {
		fmt.Fprintf(out, "models.json  ok  %s\n", filepath.Join(root, "models.json"))
	}
	if installed == 0 {
		return fmt.Errorf("no memory dictionary could be installed for %s", root)
	}
	if missing > 0 {
		return fmt.Errorf("some memory dictionaries are still missing under %s", filepath.Join(root, memory.DictionaryDir))
	}
	return nil
}

// runAssetsStatus reports, for every dictionary this build declares, where a
// search reads it from and whether it is there, then the model catalog's
// location. Resolving an entry installs a missing one where it can, so the
// report doubles as a repair; it exits non-zero while any dictionary is still
// missing.
func runAssetsStatus(cmd *cobra.Command, _ []string) error {
	out := cmd.OutOrStdout()
	root, err := home.Root()
	if err != nil {
		return err
	}
	sourceDir, err := assetsSourceDir()
	if err != nil {
		return err
	}
	available := true
	for _, status := range memory.DictionariesStatus(root, sourceDir) {
		if status.Err != nil {
			available = false
			fmt.Fprintf(out, "%s  missing\n", status.File.Name)
			continue
		}
		fmt.Fprintf(out, "%s  ok  %s\n", status.File.Name, status.Path)
	}
	fmt.Fprintf(out, "models.json  %s\n", modelsJSONLocation(root))
	if !available {
		return fmt.Errorf("some memory search dictionaries are missing; run `forebrain assets install`")
	}
	return nil
}

// assetsSourceDir is where the assets commands look for a copy to install from:
// a developer's FOREBRAIN_ASSETS_DIR when set, otherwise the directory beside
// the running binary — the same place the runtime reads models.json and the
// tokenizers from. The dictionaries live in a dict subdirectory of it.
func assetsSourceDir() (string, error) {
	dir, err := llm.AssetsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, memory.DictionaryDir), nil
}

// modelsJSONLocation names where the model catalog is read from, or that it is
// missing. It prefers the copy beside the binary, as the runtime does.
func modelsJSONLocation(root string) string {
	if dir, err := llm.AssetsDir(); err == nil {
		if path := filepath.Join(dir, "models.json"); fileExists(path) {
			return path
		}
	}
	if path := filepath.Join(root, "models.json"); fileExists(path) {
		return path
	}
	return "missing"
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
