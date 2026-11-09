package home

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	LogsDir           = "logs"
	SkillsDir         = "skills"
	WorkspaceDirName  = "workspace"
	DefaultConfigFile = "forebrain.yaml"
	// LegacyEnvFile is the file a home used to keep its ${NAME} values in. Its
	// semantics and every reader are gone (owner 2026-11-07, plan 268); the
	// name survives only so the one-time migration can find the file, read it
	// once and move its values into the state database.
	LegacyEnvFile = ".env"
)

var layoutDirs = []string{
	"state",
	LogsDir,
	SkillsDir, WorkspaceDirName,
	filepath.Join(WorkspaceDirName, "skills"),
	filepath.Join(WorkspaceDirName, "files"),
}

func Root() (string, error) {
	for _, k := range []string{"FOREBRAIN_HOME"} {
		if v := os.Getenv(k); v != "" {
			return filepath.Clean(v), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".forebrain"), nil
}

func Ensure(root string) error {
	if root == "" {
		return errors.New("empty root")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	for _, rel := range layoutDirs {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			return err
		}
	}
	if err := seedWorkspace(root); err != nil {
		return err
	}
	return nil
}

func DefaultConfigPath(root string) string {
	return filepath.Join(root, DefaultConfigFile)
}

// LegacyEnvPath is where a home used to keep its .env. Only the one-time
// migration reads it (plan 268).
func LegacyEnvPath(root string) string {
	return filepath.Join(root, LegacyEnvFile)
}

func ConfigPath(root string) string {
	return DefaultConfigPath(root)
}

func ResolveConfigPath(root string) (string, error) {
	names := []string{"forebrain.yaml", "forebrain.yml", "forebrain.json"}
	for _, name := range names {
		p := filepath.Join(root, name)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.IsDir() {
			continue
		}
		return p, nil
	}
	return "", fmt.Errorf("no config in %s (try %s or forebrain.json)", root, DefaultConfigFile)
}
