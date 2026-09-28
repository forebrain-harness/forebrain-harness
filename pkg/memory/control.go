package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ClearRootContents(root string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return fmt.Errorf("memories root is required")
	}
	info, err := os.Lstat(root)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to clear symlinked memories root %s", root)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func validateClearRoot(root, label string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return fmt.Errorf("%s is required", label)
	}
	info, err := os.Lstat(root)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to clear symlinked %s %s", label, root)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Clear empties one scope: its memory folder's contents, and the state
// directory holding its rollout evidence and consolidation watermark.
func Clear(root Root) error {
	if err := validateClearRoot(root.MemoryRoot, "memories root"); err != nil {
		return err
	}
	if err := validateClearRoot(root.stateScopeRoot(), "scope state root"); err != nil {
		return err
	}
	if err := ClearRootContents(root.MemoryRoot); err != nil {
		return err
	}
	return clearScopeState(root)
}
