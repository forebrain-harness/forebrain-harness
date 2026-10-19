package config

import (
	"log/slog"
	"os"
	"path/filepath"
)

// renameFile is os.Rename; tests replace it to make the replace step fail.
var renameFile = os.Rename

// writeFileAtomic replaces path with data so no reader ever sees a partial
// file: the bytes go to a sibling temp file, are synced, and the temp file is
// renamed over the target.
//
// Three behaviours keep it a drop-in for os.WriteFile on a config file:
//   - a symlinked path is written through to its target, so a dotfiles-managed
//     link stays a link;
//   - an existing file keeps its permission bits (perm only applies to a file
//     that does not exist yet);
//   - where a sibling cannot be created or the rename is refused (a read-only
//     directory, a bind-mounted single file in a container), it falls back to
//     the in-place write the file always had, and says so once in the log.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	if info, err := os.Stat(target); err == nil {
		perm = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-")
	if err != nil {
		return writeInPlace(target, data, perm, err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := renameFile(name, target); err != nil {
		_ = os.Remove(name)
		return writeInPlace(target, data, perm, err)
	}
	return nil
}

func writeInPlace(target string, data []byte, perm os.FileMode, cause error) error {
	slog.Warn("config: atomic replace unavailable, writing in place", "path", target, "err", cause)
	return os.WriteFile(target, data, perm)
}
