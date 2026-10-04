// File tools: read, write, edit, patching, locking, and edit style.
package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
)

var blockedReadDevicePaths = map[string]struct{}{
	"/dev/zero":    {},
	"/dev/random":  {},
	"/dev/urandom": {},
	"/dev/full":    {},
	"/dev/stdin":   {},
	"/dev/tty":     {},
	"/dev/console": {},
	"/dev/stdout":  {},
	"/dev/stderr":  {},
	"/dev/fd/0":    {},
	"/dev/fd/1":    {},
	"/dev/fd/2":    {},
}

// Offset and Limit are optional: resolveReadFileRange treats zero as "not
// given" (start of file / DefaultPageLimit). Tagging them `omitempty` keeps the
// schema honest about that instead of forcing the model to invent a page window
// for a file whose length it has not seen.
type FileReadInput struct {
	FilePath string `json:"file_path" jsonschema:"description=Absolute or workspace-relative file path."`
	Offset   int    `json:"offset,omitempty" jsonschema:"minimum=0" jsonschema_description:"Start line offset (0-based). Omit to start at the first line."`
	Limit    int    `json:"limit,omitempty" jsonschema:"minimum=1" jsonschema_description:"Max lines to return (capped at 200). Omit for the default page size."`
}

func NewFileReadTool(st *State) (*llm.Tool, error) {
	return llm.NewTool(
		"read_file",
		"Use when you need a file's contents, or before any write/edit (required first). Read a text file with a 0-based offset and limit. Read the smallest useful span.",
		func(ctx context.Context, in *FileReadInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if in == nil {
				in = &FileReadInput{}
			}
			resolution, err := authorizeRead(ctx, st, "read_file", in.FilePath)
			if err != nil {
				return "", err
			}
			abs := resolution.Abs
			skill, skillPath, skillRead := st.LoadedSkillMainFile(abs)
			skillName := strings.TrimSpace(skill.Name)
			// Preserve the skill identity even when the read itself fails. The
			// completion event then has enough information to render this as the
			// dedicated Skill card instead of falling back to a read_file card.
			if skillRead {
				CaptureToolOutput(ctx, skillReadOutput(skillName, skillPath))
			}
			raw, err := os.ReadFile(abs)
			if err != nil {
				return "", missingSkillFileError(st, in.FilePath, err)
			}
			if err := validateTextReadableFile(abs, raw); err != nil {
				return "", err
			}
			fi, err := statFile(abs)
			if err == nil {
				st.RememberRead(abs, fi.ModTime(), fi.Size(), raw)
			}
			if observer := st.ReadObserver(); observer != nil {
				observer(ctx, abs, raw)
			}
			totalLines, err := countLinesBytes(ctx, raw)
			if err != nil {
				return "", err
			}
			// Resolve offset/limit against the file's actual line count.
			// Out-of-range values are clamped so reads never error.
			// The limit is always capped at DefaultPageLimit (200) — no
			// exceptions based on file size.
			offset, limit, autoPaged := resolveReadFileRange(totalLines, in.Offset, in.Limit)
			var out string
			var lineCount int
			if offset >= totalLines {
				// Offset is at or beyond end of file: return a friendly
				// message instead of an "offset out of range" error.
				out = fmt.Sprintf("No content at offset %d: file has %d line(s).", in.Offset, totalLines)
				lineCount = 0
			} else {
				out, lineCount, err = sliceWithLineNumbersBytes(ctx, raw, offset, limit)
				if err != nil {
					return "", err
				}
			}
			if note := skillPathRepairNote(resolution); note != "" {
				out = note + "\n\n" + out
			}
			outMap := map[string]any{
				"abs_path":      abs,
				"file_path":     in.FilePath,
				"output_bytes":  len(out),
				"preview_text":  out,
				"preview_kind":  "file",
				"source_length": len(out),
				"result_lines":  lineCount,
				"offset":        offset,
				"total_lines":   totalLines,
			}
			if skillRead {
				for key, value := range skillReadOutput(skillName, skillPath) {
					outMap[key] = value
				}
			}
			if limit > 0 {
				outMap["limit"] = limit
			}
			if resolution.RepairedFrom != "" {
				outMap["repaired_from"] = resolution.RepairedFrom
			}
			if autoPaged {
				outMap["auto_paged"] = true
				nextOffset := offset + lineCount
				if nextOffset < totalLines {
					outMap["next_offset"] = nextOffset
				}
			}
			CaptureToolOutput(ctx, outMap)
			return out, nil
		},
	)
}

// authorizeRead settles whether toolName may read filePath, the same way for
// every reading tool: path resolution against the readable roots, blocking
// device paths, the FOREBRAIN_HOME approval, and deny rules.
func authorizeRead(ctx context.Context, st *State, toolName, filePath string) (readPathResolution, error) {
	resolution, err := resolveReadFilePath(ctx, st, filePath)
	if err != nil {
		if errors.Is(err, ErrPathNotAllowed) {
			CaptureToolError(ctx, err)
		}
		return readPathResolution{}, err
	}
	abs := resolution.Abs
	if isBlockedReadDevicePath(abs) {
		return readPathResolution{}, fmt.Errorf("refusing to read blocking or infinite device path")
	}
	// FOREBRAIN_HOME is asked about rather than refused -- unless the user
	// has already approved a write to this very file, in which case the
	// question was asked and answered. An embedder with no approval hook
	// has nobody to ask, so there the read stays a refusal instead of
	// happening unattended.
	if reason := st.ProtectedReadReason(abs); reason != "" && ApprovedActionIDFromContext(ctx) == "" {
		hook := st.ActionHook()
		if hook == nil {
			err := fmt.Errorf("%w: %s is inside FOREBRAIN_HOME and there is no approval path to ask about it", ErrPathNotAllowed, abs)
			CaptureToolError(ctx, err)
			return readPathResolution{}, err
		}
		payload := map[string]any{"file_path": filePath, "resolved_file_path": abs}
		markProtectedApproval(payload, reason)
		id, pending, err := hook(ctx, toolName, payload)
		if err != nil {
			return readPathResolution{}, err
		}
		if pending && id != "" {
			CaptureToolRequiresAction(ctx, id, toolName, map[string]any{"requires_action": true})
			return readPathResolution{}, &RequiresActionError{ActionID: id, ActionKind: toolName, ToolName: toolName, ToolInput: payload}
		}
	}
	if st.ReadPathDenied(abs) && !st.PathUnderLoadedSkillRoot(abs) {
		CaptureToolError(ctx, ErrPathReadDenied)
		return readPathResolution{}, fmt.Errorf("%w: %s", ErrPathReadDenied, abs)
	}
	return resolution, nil
}

func skillReadOutput(name, path string) map[string]any {
	return map[string]any{
		"preview_kind": "skill",
		"skill_name":   strings.TrimSpace(name),
		"skill_path":   filepath.Clean(path),
	}
}

// skillPathRepairNote tells the model which file it actually got. A repair the
// model is never told about leaves it believing the path it invented exists, so
// every later reference to that skill — a relative reference resolved against
// the wrong directory, a shell command, the path it quotes back to the user —
// repeats the same mistake somewhere nothing repairs it.
func skillPathRepairNote(resolution readPathResolution) string {
	if resolution.RepairedFrom == "" {
		return ""
	}
	return fmt.Sprintf("note: %s does not exist, so the %q skill's file at %s was read instead — that skill's files all live under %s.",
		resolution.RepairedFrom, resolution.Skill.Name, resolution.Abs, resolution.Skill.RootDir)
}

// missingSkillFileError says where a skill actually lives when the path that
// was not there was reaching for one. The catalog is the only reason the model
// knows these paths at all, so a bare "no such file" about a skill file is a
// dead end it cannot act on, while the skill's real root is a path it can read
// on the next call.
func missingSkillFileError(st *State, requested string, err error) error {
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	item, ok := st.loadedSkillForMissingPath(requested)
	if !ok {
		return err
	}
	return fmt.Errorf("%w (the %q skill's files live under %s)", err, item.Name, item.RootDir)
}

func isBlockedReadDevicePath(filePath string) bool {
	if _, ok := blockedReadDevicePaths[filePath]; ok {
		return true
	}
	if strings.HasPrefix(filePath, "/proc/") && (strings.HasSuffix(filePath, "/fd/0") || strings.HasSuffix(filePath, "/fd/1") || strings.HasSuffix(filePath, "/fd/2")) {
		return true
	}
	return false
}

func validateTextReadableFile(abs string, raw []byte) error {
	ext := strings.ToLower(filepath.Ext(abs))
	if ext == ".pdf" {
		return fmt.Errorf("read_file cannot read PDF files as text")
	}
	if isImageFileExtension(ext) {
		return fmt.Errorf("read_file cannot read image files as text")
	}
	if isBinaryFileExtension(ext) || looksLikeBinaryContent(raw) {
		if ext == "" {
			return fmt.Errorf("read_file cannot read binary files")
		}
		return fmt.Errorf("read_file cannot read binary %s files", ext)
	}
	return nil
}

func isImageFileExtension(ext string) bool {
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".ico", ".tiff", ".tif", ".heic", ".avif":
		return true
	default:
		return false
	}
}

func isBinaryFileExtension(ext string) bool {
	switch ext {
	case ".zip", ".gz", ".tgz", ".bz2", ".xz", ".7z", ".rar",
		".tar", ".jar", ".war", ".class", ".wasm",
		".exe", ".dll", ".dylib", ".so", ".a", ".o",
		".bin", ".dat", ".sqlite", ".db",
		".mp3", ".mp4", ".mov", ".avi", ".mkv", ".webm", ".wav",
		".ttf", ".otf", ".woff", ".woff2":
		return true
	default:
		return false
	}
}

func looksLikeBinaryContent(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	limit := len(raw)
	if limit > 8192 {
		limit = 8192
	}
	return bytes.IndexByte(raw[:limit], 0) >= 0
}

func sliceWithLineNumbersBytes(ctx context.Context, raw []byte, offset, limit int) (string, int, error) {
	if len(raw) == 0 {
		return "File is empty.", 1, nil
	}
	out, _, err := sliceWithLineNumbersFromReader(ctx, bytes.NewReader(raw), offset, limit)
	if err != nil {
		return "", 0, err
	}
	lineCount := 0
	if strings.TrimSpace(out) != "" {
		lineCount = strings.Count(strings.TrimRight(out, "\n"), "\n") + 1
	}
	return out, lineCount, nil
}

func countLinesBytes(ctx context.Context, raw []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	totalLines := CountLinesBytes(raw)
	if totalLines == 0 {
		return 1, nil
	}
	return totalLines, nil
}

// resolveReadFileRange normalizes the raw offset/limit inputs against the
// file's actual line count, returning the effective values to use for reading.
//
//   - A negative or too-large offset is clamped to [0, totalLines].
//   - A negative limit is treated as 0 (no limit provided).
//   - When no limit is provided (limit == 0), the default of
//     tool.DefaultPageLimit (200) is applied unconditionally.
//   - Any explicit limit greater than tool.DefaultPageLimit is also
//     capped at DefaultPageLimit — the hard 200-line ceiling applies to
//     every read, regardless of file size.
//   - The resolved limit is further reduced so it never runs past EOF.
//   - autoPaged is true when the resolved window does not reach EOF, signaling
//     the caller that more content follows at next_offset.
func resolveReadFileRange(totalLines, inOffset, inLimit int) (offset, limit int, autoPaged bool) {
	offset = inOffset
	if offset < 0 {
		offset = 0
	}
	if offset > totalLines {
		offset = totalLines
	}

	limit = inLimit
	if limit < 0 {
		limit = 0
	}
	// Always apply DefaultPageLimit: as the default when none was given, and
	// as a hard ceiling when the caller requests more.
	if limit <= 0 || limit > DefaultPageLimit {
		limit = DefaultPageLimit
	}
	if limit > 0 && offset+limit > totalLines {
		limit = totalLines - offset
		if limit < 0 {
			limit = 0
		}
	}

	if offset+limit < totalLines {
		autoPaged = true
	}
	return offset, limit, autoPaged
}

func sliceWithLineNumbersFromReader(ctx context.Context, r io.Reader, offset, limit int) (out string, totalLines int, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if offset < 0 {
		return "", 0, fmt.Errorf("offset must be >= 0")
	}
	if limit < 0 {
		return "", 0, fmt.Errorf("limit must be >= 0")
	}
	var b strings.Builder
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	line := 0
	wrote := 0
	for sc.Scan() {
		line++
		if err := ctx.Err(); err != nil {
			return "", line, err
		}
		if line <= offset {
			continue
		}
		if limit > 0 && wrote >= limit {
			continue
		}
		fmt.Fprintf(&b, "%d|%s\n", line, sc.Text())
		wrote++
	}
	if err := sc.Err(); err != nil {
		return "", line, err
	}
	totalLines = line
	if totalLines == 0 {
		return "File is empty.", 0, nil
	}
	s := b.String()
	if s == "" {
		// Offset is at or beyond end of file (all lines were skipped).
		// Return an empty result rather than erroring so callers can rely on
		// clamped ranges alone.
		return "", totalLines, nil
	}
	return strings.TrimRight(s, "\n"), totalLines, nil
}

// codeIntelOf returns the language-server runtime a tool should report edits
// and reads to; nil when this runner has none.
func codeIntelOf(rt *AgentToolRuntime) CodeIntelligence {
	if rt == nil {
		return nil
	}
	return rt.CodeIntel
}

// reportEditDiagnostics asks the language-server runtime what an edit
// introduced (spec §8.3) and returns the text to append to the tool result.
// It records the summary on output for the surfaces. A nil runtime, or an
// edit nothing covers, returns "".
func reportEditDiagnostics(ctx context.Context, ci CodeIntelligence, output map[string]any, changes []FileChange) string {
	if ci == nil || len(changes) == 0 {
		return ""
	}
	delta := ci.DidWrite(ctx, llm.AgentSessionIDFromContext(ctx), changes)
	if delta.Empty() {
		return ""
	}
	if output != nil {
		output["lsp_diagnostics"] = delta.Summary
	}
	return delta.Text
}

type FileWriteInput struct {
	FilePath string `json:"file_path" jsonschema:"description=Absolute or workspace-relative file path."`
	Content  string `json:"content" jsonschema:"description=New full content of the file."`
}

func NewFileWriteTool(st *State, rt *AgentToolRuntime) (*llm.Tool, error) {
	return llm.NewTool(
		"write_file",
		"Use when creating a new file or fully replacing one's contents (writes atomically). For a targeted change prefer edit_file. Overwriting an existing file requires a prior read.",
		func(ctx context.Context, in *FileWriteInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if in == nil {
				in = &FileWriteInput{}
			}
			// GuardTool still runs (it carries the typed-subagent read-only
			// guard); in plan mode it now passes write_file through and defers
			// the write-path decision to GuardWrite below.
			if err := st.GuardTool(ctx, "write_file"); err != nil {
				return "", err
			}
			// In plan mode the only writable path is the session plan file,
			// which lives outside the workspace roots, and the ActionHook is
			// skipped for it. Every other path is settled here, including the
			// protected ones: those raise the approval below rather than fail.
			res, err := resolveGuardedWritePath(ctx, st, in.FilePath)
			if err != nil {
				CaptureToolError(ctx, err)
				return "", err
			}
			abs, planWrite, protected := res.Abs, res.PlanWrite, res.Protected
			if hook := st.ActionHook(); hook != nil && !planWrite {
				if ApprovedActionIDFromContext(ctx) == "" && (protected != "" || (!PolicyApprovedFromContext(ctx) && !st.PathGranted(abs, safety.FileSystemAccessWrite, ConversationSessionIDFromContext(ctx), RunIDFromContext(ctx)))) {
					payload := map[string]any{"file_path": in.FilePath, "resolved_file_path": abs, "content": in.Content}
					markProtectedApproval(payload, protected)
					id, ok, err := hook(ctx, "write_file", payload)
					if err != nil {
						return "", err
					}
					if ok && id != "" {
						CaptureToolRequiresAction(ctx, id, "write_file", map[string]any{"requires_action": true})
						return "", &RequiresActionError{ActionID: id, ActionKind: "write_file", ToolName: "write_file", ToolInput: payload}
					}
				}
			}
			// Past the gate the write is authorized, even if the read it still
			// owes has not happened yet. Record it so that read is not a second
			// question about a change the user has already agreed to.
			if protected != "" {
				st.RememberApprovedWrite(abs)
			}
			if err := st.preflightDestructive(ctx, "write_file", in.Content, abs, "write_file", in); err != nil {
				return "", err
			}
			unlock := lockFileWrite(abs)
			defer unlock()
			var before []byte
			if fi, err := statFile(abs); err == nil && fi != nil {
				rs, ok := st.GetReadState(abs)
				if !ok {
					return "", fmt.Errorf("must read_file before overwriting")
				}
				if fi.ModTime().After(rs.ModTime) || fi.Size() != rs.Size {
					return "", fmt.Errorf("file changed since last read")
				}
				prev, err := os.ReadFile(abs)
				if err != nil {
					return "", err
				}
				if !readStateMatchesContent(rs, prev) {
					return "", fmt.Errorf("file changed since last read")
				}
				before = append([]byte(nil), prev...)
				_ = st.SnapshotBeforeWrite(abs, prev)
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return "", err
			}
			if err := atomicWrite(abs, []byte(in.Content), 0o644); err != nil {
				return "", err
			}
			fi, err := statFile(abs)
			if err == nil && fi != nil {
				st.RememberWrite(abs, fi.ModTime(), fi.Size(), []byte(in.Content))
			}
			output := map[string]any{
				"status":   "ok",
				"abs_path": abs,
			}
			if res.RepairedFrom != "" {
				output["repaired_from"] = res.RepairedFrom
			}
			attachTurnDiff(output, abs, before, []byte(in.Content))
			// Diagnostics are collected only after the write landed, so the wait
			// window can never fail or delay the write itself (spec §8.3.5).
			diag := reportEditDiagnostics(ctx, codeIntelOf(rt), output, []FileChange{{AbsPath: abs, Before: before, After: []byte(in.Content)}})
			CaptureToolOutput(ctx, output)
			result := "ok"
			if note := skillWritePathRepairNote(res); note != "" {
				result = note + "\n\nok"
			}
			if diag != "" {
				result += "\n\n" + diag
			}
			return result, nil
		},
	)
}

func atomicWrite(target string, content []byte, mode os.FileMode) error {
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir := filepath.Dir(target)
	tmp := fmt.Sprintf("%s.tmp.%d", filepath.Base(target), time.Now().UnixNano())
	tmpPath := filepath.Join(dir, tmp)
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, werr := f.Write(content)
	cerr := f.Close()
	if werr != nil {
		_ = os.Remove(tmpPath)
		return werr
	}
	if cerr != nil {
		_ = os.Remove(tmpPath)
		return cerr
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

var ErrOldStringNotFound = errors.New("old_string not found")

type FileEditInput struct {
	FilePath   string `json:"file_path" jsonschema:"description=Absolute or workspace-relative file path."`
	OldString  string `json:"old_string" jsonschema:"description=Exact text to replace. Must match file content."`
	NewString  string `json:"new_string" jsonschema:"description=Replacement text."`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"description=Replace all occurrences if true."`
}

type FileEditOptions struct {
	PrepareLocked func(absPath string, current []byte, in *FileEditInput) error
	// CodeIntel receives the edit for after-edit diagnostics (spec §8.3). The
	// edit tool is built without a runtime, so the language-server runtime is
	// handed in here instead.
	CodeIntel CodeIntelligence
}

func NewFileEditTool(st *State) (*llm.Tool, error) {
	return NewFileEditToolWithOptions(st, FileEditOptions{})
}

func NewFileEditToolWithOptions(st *State, opts FileEditOptions) (*llm.Tool, error) {
	return llm.NewTool(
		"edit_file",
		"Use when making a targeted change to an existing file: replaces an exact, unique string (set replace_all for repeated text). Requires a prior read; old_string must match on-disk content exactly, including whitespace.",
		func(ctx context.Context, in *FileEditInput) (string, error) {
			if st == nil {
				return "", fmt.Errorf("nil state")
			}
			if in == nil {
				in = &FileEditInput{}
			}
			// GuardTool still runs (it carries the typed-subagent read-only
			// guard); in plan mode it now passes edit_file through and defers
			// the write-path decision to GuardWrite below.
			if err := st.GuardTool(ctx, "edit_file"); err != nil {
				return "", err
			}
			// In plan mode the only writable path is the session plan file,
			// which lives outside the workspace roots, and the ActionHook is
			// skipped for it. Every other path is settled here, including the
			// protected ones: those raise the approval below rather than fail.
			res, err := resolveGuardedWritePath(ctx, st, in.FilePath)
			if err != nil {
				CaptureToolError(ctx, err)
				return "", err
			}
			abs, planWrite, protected := res.Abs, res.PlanWrite, res.Protected
			if hook := st.ActionHook(); hook != nil && !planWrite {
				if ApprovedActionIDFromContext(ctx) == "" && (protected != "" || (!PolicyApprovedFromContext(ctx) && !st.PathGranted(abs, safety.FileSystemAccessWrite, ConversationSessionIDFromContext(ctx), RunIDFromContext(ctx)))) {
					payload := map[string]any{
						"file_path":          in.FilePath,
						"resolved_file_path": abs,
						"old_string":         in.OldString,
						"new_string":         in.NewString,
						"replace_all":        in.ReplaceAll,
					}
					markProtectedApproval(payload, protected)
					id, ok, err := hook(ctx, "edit_file", payload)
					if err != nil {
						return "", err
					}
					if ok && id != "" {
						CaptureToolRequiresAction(ctx, id, "edit_file", map[string]any{"requires_action": true})
						return "", &RequiresActionError{ActionID: id, ActionKind: "edit_file", ToolName: "edit_file", ToolInput: payload}
					}
				}
			}
			// Past the gate the edit is authorized, even if the read it still
			// owes has not happened yet. Record it so that read is not a second
			// question about a change the user has already agreed to.
			if protected != "" {
				st.RememberApprovedWrite(abs)
			}
			if err := st.preflightDestructive(ctx, "edit_file", in.OldString+"\n"+in.NewString, abs, "edit_file", in); err != nil {
				return "", err
			}
			unlock := lockFileWrite(abs)
			defer unlock()
			rs, ok := st.GetReadState(abs)
			if !ok {
				return "", fmt.Errorf("must read_file before edit")
			}
			fi, err := statFile(abs)
			if err != nil {
				return "", err
			}
			if fi.ModTime().After(rs.ModTime) || fi.Size() != rs.Size {
				return "", fmt.Errorf("file changed since last read")
			}
			raw, err := os.ReadFile(abs)
			if err != nil {
				return "", err
			}
			if !readStateMatchesContent(rs, raw) {
				return "", fmt.Errorf("file changed since last read")
			}
			if opts.PrepareLocked != nil {
				if err := opts.PrepareLocked(abs, raw, in); err != nil {
					return "", err
				}
			}
			if strings.TrimSpace(in.OldString) == "" {
				return "", fmt.Errorf("old_string required")
			}
			_ = st.SnapshotBeforeWrite(abs, raw)
			src := string(raw)
			oldString := in.OldString
			actualOld := findActualEditString(src, oldString)
			if actualOld != "" {
				oldString = actualOld
			}
			newString := preserveTextEditQuoteStyle(in.OldString, oldString, in.NewString)
			newString = preserveIndentationStyle(in.OldString, oldString, newString)
			cnt := strings.Count(src, oldString)
			if cnt == 0 {
				return "", fmt.Errorf("%w: old_string does not match the current file content (the file itself is unchanged per the last read_file). Re-read the exact lines and copy the text verbatim, watching for tabs vs spaces and line endings", ErrOldStringNotFound)
			}
			if cnt > 1 && !in.ReplaceAll {
				return "", fmt.Errorf("old_string is not unique; set replace_all or include more context")
			}
			var out string
			if in.ReplaceAll {
				out = strings.ReplaceAll(src, oldString, newString)
			} else {
				out = strings.Replace(src, oldString, newString, 1)
			}
			if err := atomicWrite(abs, []byte(out), 0o644); err != nil {
				return "", err
			}
			fi2, err := statFile(abs)
			if err == nil && fi2 != nil {
				st.RememberWrite(abs, fi2.ModTime(), fi2.Size(), []byte(out))
			}
			output := map[string]any{
				"status":   "ok",
				"abs_path": abs,
				"replaced": cnt,
			}
			if res.RepairedFrom != "" {
				output["repaired_from"] = res.RepairedFrom
			}
			attachTurnDiff(output, abs, raw, []byte(out))
			// Diagnostics are collected only after the write landed, so the wait
			// window can never fail or delay the write itself (spec §8.3.5).
			diag := reportEditDiagnostics(ctx, opts.CodeIntel, output, []FileChange{{AbsPath: abs, Before: raw, After: []byte(out)}})
			CaptureToolOutput(ctx, output)
			result := "ok"
			if note := skillWritePathRepairNote(res); note != "" {
				result = note + "\n\nok"
			}
			if diag != "" {
				result += "\n\n" + diag
			}
			return result, nil
		},
	)
}

type applyPatchOpKind string

const (
	applyPatchUpdate applyPatchOpKind = "update"
	applyPatchAdd    applyPatchOpKind = "add"
	applyPatchDelete applyPatchOpKind = "delete"
)

type applyPatchOperation struct {
	Kind     applyPatchOpKind
	Path     string
	MovePath string
	Hunks    []applyPatchHunk
	Body     string
}

type applyPatchHunk struct {
	Lines []applyPatchLine
}

type applyPatchLine struct {
	Kind byte
	Text string
}

type applyPatchFilePlan struct {
	AbsPath    string
	OpPath     string
	MoveAbs    string
	MoveOpPath string
	MoveBefore []byte
	Kind       applyPatchOpKind
	Before     []byte
	After      []byte
	Delete     bool
}

func maybeHandleApplyPatchCommand(ctx context.Context, st *State, rt *AgentToolRuntime, cmd string) (bool, string, error) {
	patch, ok, err := extractApplyPatchBody(cmd)
	if !ok || err != nil {
		return ok, "", err
	}
	ops, err := parseApplyPatch(patch)
	if err != nil {
		return true, "", err
	}
	out, err := applyPatchOperations(ctx, st, rt, patch, ops)
	return true, out, err
}

// ShellCommandIsApplyPatch reports whether command is a complete standalone
// apply_patch invocation. It uses the same parser as execution so approval
// routing cannot disagree with the command that will run.
func ShellCommandIsApplyPatch(command string) bool {
	patch, ok, err := extractApplyPatchBody(command)
	if !ok || err != nil {
		return false
	}
	_, err = parseApplyPatch(patch)
	return err == nil
}

// ApplyPatchWrite is one file an apply_patch would write, with the content the
// write would produce.
type ApplyPatchWrite struct {
	// Path is the absolute path the patch writes to, and OpPath the path the
	// patch spelled.
	Path   string
	OpPath string
	// Content is the file's content after the patch. Delete marks a removal,
	// which has no content to inspect.
	Content []byte
	Delete  bool
}

// PreviewApplyPatchWrites reports what an apply_patch command would write,
// without writing anything. It is the read-only counterpart of the apply path:
// both parse the same patch and resolve paths the same way, so a caller that
// has to judge the resulting content — the skill write guard, which refuses a
// SKILL.md that would land invisible — sees exactly what execution would
// produce. A patch that does not parse, or whose hunks do not apply, returns
// an error; the caller lets the real tool report its own failure in that case.
func PreviewApplyPatchWrites(ctx context.Context, st *State, command string) ([]ApplyPatchWrite, error) {
	patch, ok, err := extractApplyPatchBody(command)
	if !ok || err != nil {
		return nil, fmt.Errorf("not an apply_patch command")
	}
	ops, err := parseApplyPatch(patch)
	if err != nil {
		return nil, err
	}
	out := make([]ApplyPatchWrite, 0, len(ops))
	after := make(map[string][]byte, len(ops))
	for _, op := range ops {
		res, err := resolveApplyPatchPath(ctx, st, op.Path)
		if err != nil {
			return nil, err
		}
		write := ApplyPatchWrite{Path: res.Abs, OpPath: op.Path}
		switch op.Kind {
		case applyPatchAdd:
			write.Content = []byte(op.Body)
		case applyPatchUpdate:
			src := after[res.Abs]
			if src == nil {
				if raw, readErr := os.ReadFile(res.Abs); readErr == nil {
					src = raw
				}
			}
			next, applyErr := applyPatchHunks(string(src), op.Hunks)
			if applyErr != nil {
				return nil, applyErr
			}
			write.Content = []byte(next)
			if op.MovePath != "" {
				moveRes, moveErr := resolveApplyPatchPath(ctx, st, op.MovePath)
				if moveErr != nil {
					return nil, moveErr
				}
				out = append(out, ApplyPatchWrite{Path: moveRes.Abs, OpPath: op.MovePath, Content: write.Content})
			}
		case applyPatchDelete:
			write.Delete = true
		}
		after[res.Abs] = write.Content
		out = append(out, write)
	}
	return out, nil
}

func extractApplyPatchBody(cmd string) (string, bool, error) {
	cmd = strings.ReplaceAll(cmd, "\r\n", "\n")
	cmd = strings.ReplaceAll(cmd, "\r", "\n")
	lines := strings.Split(cmd, "\n")
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	if start >= len(lines) {
		return "", false, nil
	}
	first := strings.TrimSpace(lines[start])
	if !strings.HasPrefix(first, "apply_patch") {
		return "", false, nil
	}
	if len(first) > len("apply_patch") {
		next := first[len("apply_patch")]
		if next != ' ' && next != '\t' && next != '<' {
			return "", false, nil
		}
	}
	if idx := strings.Index(first, "<<"); idx >= 0 {
		delim := strings.TrimSpace(first[idx+2:])
		if strings.HasPrefix(delim, "-") {
			delim = strings.TrimSpace(delim[1:])
		}
		fields := strings.Fields(delim)
		if len(fields) == 0 {
			return "", true, fmt.Errorf("apply_patch heredoc delimiter required")
		}
		delim = strings.Trim(fields[0], "'\"")
		if delim == "" {
			return "", true, fmt.Errorf("apply_patch heredoc delimiter required")
		}
		for i := start + 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == delim {
				for j := i + 1; j < len(lines); j++ {
					if strings.TrimSpace(lines[j]) != "" {
						return "", true, fmt.Errorf("apply_patch heredoc must not contain trailing shell commands")
					}
				}
				return strings.Join(lines[start+1:i], "\n"), true, nil
			}
		}
		return "", true, fmt.Errorf("apply_patch heredoc terminator %q not found", delim)
	}
	rest := strings.Join(lines[start+1:], "\n")
	if strings.HasPrefix(strings.TrimSpace(rest), "*** Begin Patch") {
		return rest, true, nil
	}
	return "", true, fmt.Errorf("apply_patch command must use a heredoc containing a patch")
}

func parseApplyPatch(body string) ([]applyPatchOperation, error) {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) || strings.TrimSpace(lines[i]) != "*** Begin Patch" {
		return nil, fmt.Errorf("apply_patch: missing *** Begin Patch marker")
	}
	i++
	var ops []applyPatchOperation
	for i < len(lines) {
		line := strings.TrimSpace(lines[i])
		switch {
		case line == "":
			i++
			continue
		case line == "*** End Patch":
			if len(ops) == 0 {
				return nil, fmt.Errorf("apply_patch: patch contains no file operations")
			}
			return ops, nil
		case strings.HasPrefix(line, "*** Update File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:"))
			op, next, err := parseApplyPatchUpdate(path, lines, i+1)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			i = next
		case strings.HasPrefix(line, "*** Add File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:"))
			op, next, err := parseApplyPatchAdd(path, lines, i+1)
			if err != nil {
				return nil, err
			}
			ops = append(ops, op)
			i = next
		case strings.HasPrefix(line, "*** Delete File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:"))
			if strings.TrimSpace(path) == "" {
				return nil, fmt.Errorf("apply_patch: delete file path required")
			}
			ops = append(ops, applyPatchOperation{Kind: applyPatchDelete, Path: cleanApplyPatchPath(path)})
			i++
		default:
			return nil, fmt.Errorf("apply_patch: unsupported patch line %q", lines[i])
		}
	}
	return nil, fmt.Errorf("apply_patch: missing *** End Patch marker")
}

func parseApplyPatchUpdate(path string, lines []string, i int) (applyPatchOperation, int, error) {
	path = cleanApplyPatchPath(path)
	if path == "" {
		return applyPatchOperation{}, i, fmt.Errorf("apply_patch: update file path required")
	}
	op := applyPatchOperation{Kind: applyPatchUpdate, Path: path}
	if i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "*** Move to:") {
			op.MovePath = cleanApplyPatchPath(strings.TrimSpace(strings.TrimPrefix(trimmed, "*** Move to:")))
			if op.MovePath == "" {
				return applyPatchOperation{}, i, fmt.Errorf("apply_patch: move destination path required")
			}
			i++
		}
	}
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "*** End Patch" || strings.HasPrefix(trimmed, "*** Update File:") || strings.HasPrefix(trimmed, "*** Add File:") || strings.HasPrefix(trimmed, "*** Delete File:") {
			break
		}
		if !strings.HasPrefix(lines[i], "@@") {
			if strings.TrimSpace(lines[i]) == "" {
				i++
				continue
			}
			return applyPatchOperation{}, i, fmt.Errorf("apply_patch: update for %s must contain @@ hunks", path)
		}
		i++
		h := applyPatchHunk{}
		for i < len(lines) {
			trimmed = strings.TrimSpace(lines[i])
			if strings.HasPrefix(lines[i], "@@") || trimmed == "*** End Patch" || strings.HasPrefix(trimmed, "*** Update File:") || strings.HasPrefix(trimmed, "*** Add File:") || strings.HasPrefix(trimmed, "*** Delete File:") {
				break
			}
			if trimmed == "*** End of File" {
				i++
				break
			}
			if strings.HasPrefix(lines[i], `\ `) {
				i++
				continue
			}
			if lines[i] == "" {
				return applyPatchOperation{}, i, fmt.Errorf("apply_patch: malformed empty hunk line for %s", path)
			}
			k := lines[i][0]
			if k != ' ' && k != '-' && k != '+' {
				return applyPatchOperation{}, i, fmt.Errorf("apply_patch: malformed hunk line for %s: %q", path, lines[i])
			}
			h.Lines = append(h.Lines, applyPatchLine{Kind: k, Text: lines[i][1:]})
			i++
		}
		if len(h.Lines) == 0 {
			return applyPatchOperation{}, i, fmt.Errorf("apply_patch: empty hunk for %s", path)
		}
		op.Hunks = append(op.Hunks, h)
	}
	if len(op.Hunks) == 0 {
		return applyPatchOperation{}, i, fmt.Errorf("apply_patch: update for %s contains no hunks", path)
	}
	return op, i, nil
}

func parseApplyPatchAdd(path string, lines []string, i int) (applyPatchOperation, int, error) {
	path = cleanApplyPatchPath(path)
	if path == "" {
		return applyPatchOperation{}, i, fmt.Errorf("apply_patch: add file path required")
	}
	var sb strings.Builder
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "*** End Patch" || strings.HasPrefix(trimmed, "*** Update File:") || strings.HasPrefix(trimmed, "*** Add File:") || strings.HasPrefix(trimmed, "*** Delete File:") {
			break
		}
		if strings.HasPrefix(lines[i], `\ `) {
			i++
			continue
		}
		if lines[i] == "" {
			return applyPatchOperation{}, i, fmt.Errorf("apply_patch: malformed empty add line for %s", path)
		}
		if lines[i][0] != '+' {
			return applyPatchOperation{}, i, fmt.Errorf("apply_patch: add file lines for %s must start with +", path)
		}
		sb.WriteString(lines[i][1:])
		sb.WriteByte('\n')
		i++
	}
	return applyPatchOperation{Kind: applyPatchAdd, Path: path, Body: sb.String()}, i, nil
}

func cleanApplyPatchPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, "'\"")
	path = filepath.Clean(path)
	path = filepath.ToSlash(path)
	if strings.HasPrefix(path, "a/") || strings.HasPrefix(path, "b/") {
		path = path[2:]
	}
	if path == "." {
		return ""
	}
	return path
}

func applyPatchOperations(ctx context.Context, st *State, rt *AgentToolRuntime, patch string, ops []applyPatchOperation) (string, error) {
	if st == nil {
		return "", fmt.Errorf("nil state")
	}
	if len(ops) == 0 {
		return "", fmt.Errorf("apply_patch: patch contains no file operations")
	}
	absByPath := make(map[string]string, len(ops))
	moveAbsByPath := make(map[string]string, len(ops))
	resolvedPaths := make([]string, 0, len(ops)*2)
	hasNonPlanWrite := false
	var protectedReasons []string
	var protectedPaths []string
	var repairNotes []string
	for _, op := range ops {
		toolName := "edit_file"
		if op.Kind == applyPatchAdd {
			toolName = "write_file"
		}
		if err := st.GuardTool(ctx, toolName); err != nil {
			return "", err
		}
		res, err := resolveApplyPatchPath(ctx, st, op.Path)
		if err != nil {
			CaptureToolError(ctx, err)
			return "", err
		}
		abs, planWrite, protected := res.Abs, res.PlanWrite, res.Protected
		repairNotes = appendProtectedReason(repairNotes, skillWritePathRepairNote(res))
		absByPath[op.Path] = abs
		resolvedPaths = append(resolvedPaths, abs)
		hasNonPlanWrite = hasNonPlanWrite || !planWrite
		protectedReasons = appendProtectedReason(protectedReasons, protected)
		if protected != "" {
			protectedPaths = append(protectedPaths, abs)
		}
		if err := st.GuardWrite(ctx, abs); err != nil {
			return "", err
		}
		if err := st.preflightDestructive(ctx, toolName, patch, abs, toolName, map[string]any{"file_path": op.Path, "operation": string(op.Kind), "apply_patch": true}); err != nil {
			return "", err
		}
		if op.MovePath != "" {
			moveRes, err := resolveApplyPatchPath(ctx, st, op.MovePath)
			if err != nil {
				CaptureToolError(ctx, err)
				return "", err
			}
			moveAbs, movePlanWrite, moveProtected := moveRes.Abs, moveRes.PlanWrite, moveRes.Protected
			repairNotes = appendProtectedReason(repairNotes, skillWritePathRepairNote(moveRes))
			moveAbsByPath[op.Path] = moveAbs
			resolvedPaths = append(resolvedPaths, moveAbs)
			hasNonPlanWrite = hasNonPlanWrite || !movePlanWrite
			protectedReasons = appendProtectedReason(protectedReasons, moveProtected)
			if moveProtected != "" {
				protectedPaths = append(protectedPaths, moveAbs)
			}
			if err := st.GuardWrite(ctx, moveAbs); err != nil {
				return "", err
			}
			if err := st.preflightDestructive(ctx, toolName, patch, moveAbs, toolName, map[string]any{"file_path": op.MovePath, "source_file_path": op.Path, "operation": "move", "apply_patch": true}); err != nil {
				return "", err
			}
		}
	}
	if hasNonPlanWrite {
		payload := map[string]any{
			"apply_patch":    true,
			"patch":          patch,
			"resolved_paths": uniqueSortedPaths(resolvedPaths),
		}
		markProtectedApproval(payload, strings.Join(protectedReasons, " "))
		if hook := st.ActionHook(); hook != nil {
			id, pending, err := hook(ctx, "apply_patch", payload)
			if err != nil {
				return "", err
			}
			if pending && id != "" {
				CaptureToolRequiresAction(ctx, id, "apply_patch", map[string]any{"requires_action": true, "apply_patch": true})
				return "", &RequiresActionError{ActionID: id, ActionKind: "apply_patch", ToolName: "apply_patch", ToolInput: payload, OriginatingToolCallID: ToolUseIDFromContext(ctx)}
			}
		} else if ApprovedActionIDFromContext(ctx) == "" && !SandboxBypassApprovedFromContext(ctx) {
			for _, path := range resolvedPaths {
				if _, err := ResolveWithinRoots(path, st.AllowedRoots()); err != nil && !st.PathGranted(path, safety.FileSystemAccessWrite, ConversationSessionIDFromContext(ctx), RunIDFromContext(ctx)) {
					return "", fmt.Errorf("%w: %s", ErrPathNotAllowed, path)
				}
			}
		}
		// Past the gate the patch is authorized. Its protected targets no longer
		// ask a second time for the reads they still owe.
		for _, path := range protectedPaths {
			st.RememberApprovedWrite(path)
		}
	}
	uniqueAbs := make([]string, 0, len(absByPath))
	seen := map[string]struct{}{}
	for _, abs := range absByPath {
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		uniqueAbs = append(uniqueAbs, abs)
	}
	for _, abs := range moveAbsByPath {
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		uniqueAbs = append(uniqueAbs, abs)
	}
	sort.Strings(uniqueAbs)
	unlocks := make([]func(), 0, len(uniqueAbs))
	for _, abs := range uniqueAbs {
		unlocks = append(unlocks, lockFileWrite(abs))
	}
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}()

	byAbs := make(map[string]*applyPatchFilePlan, len(uniqueAbs))
	for _, op := range ops {
		abs := absByPath[op.Path]
		plan := byAbs[abs]
		if plan == nil {
			plan = &applyPatchFilePlan{AbsPath: abs, OpPath: op.Path, Kind: op.Kind}
			if raw, err := os.ReadFile(abs); err == nil {
				plan.Before = append([]byte(nil), raw...)
				plan.After = append([]byte(nil), raw...)
			} else if !os.IsNotExist(err) {
				return "", err
			}
			byAbs[abs] = plan
		}
		plan.Kind = op.Kind
		switch op.Kind {
		case applyPatchAdd:
			if len(plan.Before) > 0 || fileExists(abs) {
				return "", fmt.Errorf("apply_patch: add file already exists: %s", op.Path)
			}
			plan.After = []byte(op.Body)
			plan.Delete = false
		case applyPatchUpdate:
			if plan.Delete {
				return "", fmt.Errorf("apply_patch: cannot update deleted file: %s", op.Path)
			}
			if !fileExists(abs) && len(plan.Before) == 0 {
				return "", fmt.Errorf("apply_patch: update file does not exist: %s", op.Path)
			}
			out, err := applyPatchHunks(string(plan.After), op.Hunks)
			if err != nil {
				return "", fmt.Errorf("apply_patch: %s: %w", op.Path, err)
			}
			plan.After = []byte(out)
			plan.Delete = false
			if moveAbs := moveAbsByPath[op.Path]; moveAbs != "" && moveAbs != abs {
				plan.MoveAbs = moveAbs
				plan.MoveOpPath = op.MovePath
				if raw, err := os.ReadFile(moveAbs); err == nil {
					plan.MoveBefore = append([]byte(nil), raw...)
				} else if !os.IsNotExist(err) {
					return "", err
				}
			}
		case applyPatchDelete:
			if !fileExists(abs) && len(plan.Before) == 0 {
				return "", fmt.Errorf("apply_patch: delete file does not exist: %s", op.Path)
			}
			plan.After = nil
			plan.Delete = true
		default:
			return "", fmt.Errorf("apply_patch: unsupported operation %q", op.Kind)
		}
	}

	files := make([]map[string]any, 0, len(byAbs))
	// All of one patch's files share a single diagnostics wait window (spec
	// §8.3.2): the changes are collected across the write loop and reach the
	// language-server runtime in one DidWrite call after the last write.
	var changes []FileChange
	for _, abs := range uniqueAbs {
		plan := byAbs[abs]
		if plan == nil {
			continue
		}
		if err := st.SnapshotBeforeWrite(abs, plan.Before); err != nil {
			return "", err
		}
		if plan.MoveAbs != "" {
			if err := st.SnapshotBeforeWrite(plan.MoveAbs, plan.MoveBefore); err != nil {
				return "", err
			}
		}
		if plan.Delete {
			if err := os.Remove(abs); err != nil {
				return "", err
			}
			st.RememberWrite(abs, time.Now(), 0, nil)
			changes = append(changes, FileChange{AbsPath: abs, Before: plan.Before})
		} else {
			writePath := abs
			if plan.MoveAbs != "" {
				writePath = plan.MoveAbs
			}
			if err := os.MkdirAll(filepath.Dir(writePath), 0o755); err != nil {
				return "", err
			}
			if err := atomicWrite(writePath, plan.After, 0o644); err != nil {
				return "", err
			}
			if fi, err := statFile(writePath); err == nil && fi != nil {
				st.RememberWrite(writePath, fi.ModTime(), fi.Size(), plan.After)
			}
			if plan.MoveAbs != "" {
				if err := os.Remove(abs); err != nil {
					return "", err
				}
				st.RememberWrite(abs, time.Now(), 0, nil)
				changes = append(changes, FileChange{AbsPath: abs, Before: plan.Before})
				changes = append(changes, FileChange{AbsPath: plan.MoveAbs, Before: plan.MoveBefore, After: plan.After})
			} else {
				changes = append(changes, FileChange{AbsPath: writePath, Before: plan.Before, After: plan.After})
			}
		}
		outputPath := abs
		if plan.MoveAbs != "" {
			outputPath = plan.MoveAbs
		}
		entry := map[string]any{"abs_path": outputPath, "operation": string(plan.Kind)}
		if plan.MoveAbs != "" {
			entry["source_abs_path"] = abs
			entry["move_path"] = plan.MoveOpPath
		}
		attachTurnDiff(entry, outputPath, plan.Before, plan.After)
		files = append(files, entry)
	}
	stdout := fmt.Sprintf("Applied patch to %d file(s).\n", len(files))
	// A repaired path is reported the same way a repaired read is: the model has
	// to learn which file it actually changed.
	if len(repairNotes) > 0 {
		stdout = strings.Join(repairNotes, "\n") + "\n\n" + stdout
	}
	// The summary rides on a temporary map so the one delta reaches both the
	// returned payload and the completion event the surfaces render from.
	diagOutput := map[string]any{}
	diag := reportEditDiagnostics(ctx, codeIntelOf(rt), diagOutput, changes)
	if diag != "" {
		stdout += "\n" + diag + "\n"
	}
	payload := map[string]any{
		"stdout":      stdout,
		"stderr":      "",
		"exit_code":   0,
		"apply_patch": true,
		"files":       files,
	}
	completionOutput := appendToolExecutionMetadata(ctx, st, map[string]any{
		"stdout":       stdout,
		"stderr":       "",
		"exit_code":    0,
		"stdout_bytes": len(stdout),
		"stderr_bytes": 0,
		"apply_patch":  true,
		"files":        files,
	}, rt, safety.ToolKindShell)
	if summary, ok := diagOutput["lsp_diagnostics"]; ok {
		payload["lsp_diagnostics"] = summary
		completionOutput["lsp_diagnostics"] = summary
	}
	if rt != nil && rt.YOLO {
		payload["yolo"] = true
		payload["approval_bypassed_by_yolo"] = true
	}
	CaptureToolCompletion(ctx, ToolCompletionPayload{
		Output: completionOutput,
	})
	b, _ := json.Marshal(payload)
	return string(b), nil
}

// resolveApplyPatchPath settles one operation's path the way
// resolveGuardedWritePath settles a write_file path: where it lands, whether it
// is the plan file, and the sentence the approval has to show when the target is
// protected. Two refusals stay hard because no approval repairs them — an
// explicit deny the operator configured, and another primary agent's workspace.
func resolveApplyPatchPath(ctx context.Context, st *State, path string) (writePathResolution, error) {
	if planPath, ok := planFileWriteTarget(ctx, path); ok {
		return writePathResolution{Abs: planPath, PlanWrite: true}, nil
	}
	if st == nil {
		return writePathResolution{}, fmt.Errorf("nil state")
	}
	if target, item, ok := st.LoadedSkillWriteTarget(path); ok {
		out := writePathResolution{Abs: target, Protected: loadedSkillWriteReason(target, item)}
		if normalizeRootPath(path) != target {
			out.RepairedFrom, out.Skill = strings.TrimSpace(path), item
		}
		return out, nil
	}
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return writePathResolution{}, err
	}
	abs = filepath.Clean(abs)
	if st.PathDeniedByGrant(abs, ConversationSessionIDFromContext(ctx), RunIDFromContext(ctx)) {
		return writePathResolution{}, fmt.Errorf("%w: denied by request_permissions grant: %s", ErrPathNotAllowed, abs)
	}
	if st.PathOverlapsSiblingPrimaryWorkspace(abs) {
		return writePathResolution{}, fmt.Errorf("%w: primary agent workspace isolation blocks %s", ErrPathNotAllowed, path)
	}
	volumeRoot := string(filepath.Separator)
	if volume := filepath.VolumeName(abs); volume != "" {
		volumeRoot = volume + string(filepath.Separator)
	}
	resolved, err := ResolveWithinRoots(abs, []string{volumeRoot})
	if err != nil {
		return writePathResolution{}, err
	}
	return writePathResolution{Abs: resolved, Protected: st.ProtectedWriteReason(ctx, resolved)}, nil
}

func uniqueSortedPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func applyPatchHunks(src string, hunks []applyPatchHunk) (string, error) {
	out := src
	searchStart := 0
	for _, h := range hunks {
		oldChunk, newChunk := h.oldNewChunks()
		if oldChunk == "" {
			if src != "" || searchStart != 0 {
				return "", fmt.Errorf("empty-context insertion hunk is ambiguous")
			}
			out = newChunk + out
			searchStart = len(newChunk)
			continue
		}
		idx, oldLen := findPatchChunk(out, oldChunk, searchStart)
		if idx < 0 {
			return "", fmt.Errorf("hunk context not found")
		}
		out = out[:idx] + newChunk + out[idx+oldLen:]
		searchStart = idx + len(newChunk)
	}
	return out, nil
}

func (h applyPatchHunk) oldNewChunks() (string, string) {
	var oldChunk, newChunk strings.Builder
	for _, line := range h.Lines {
		switch line.Kind {
		case ' ':
			oldChunk.WriteString(line.Text)
			oldChunk.WriteByte('\n')
			newChunk.WriteString(line.Text)
			newChunk.WriteByte('\n')
		case '-':
			oldChunk.WriteString(line.Text)
			oldChunk.WriteByte('\n')
		case '+':
			newChunk.WriteString(line.Text)
			newChunk.WriteByte('\n')
		}
	}
	return oldChunk.String(), newChunk.String()
}

func findPatchChunk(src, oldChunk string, start int) (int, int) {
	if start < 0 || start > len(src) {
		start = 0
	}
	if idx := strings.Index(src[start:], oldChunk); idx >= 0 {
		return start + idx, len(oldChunk)
	}
	if strings.HasSuffix(oldChunk, "\n") {
		trimmed := strings.TrimSuffix(oldChunk, "\n")
		if trimmed != "" {
			if idx := strings.Index(src[start:], trimmed); idx >= 0 && start+idx+len(trimmed) == len(src) {
				return start + idx, len(trimmed)
			}
			if idx := strings.Index(src, trimmed); idx >= 0 && idx+len(trimmed) == len(src) {
				return idx, len(trimmed)
			}
		}
	}
	if idx := strings.Index(src, oldChunk); idx >= 0 {
		return idx, len(oldChunk)
	}
	return -1, 0
}

type fileWriteLock struct {
	mu   sync.Mutex
	refs int
}

var fileWriteLocks = struct {
	sync.Mutex
	byPath map[string]*fileWriteLock
}{
	byPath: make(map[string]*fileWriteLock),
}

func lockFileWrite(absPath string) func() {
	path := filepath.Clean(strings.TrimSpace(absPath))
	if path == "" || path == "." {
		return func() {}
	}
	fileWriteLocks.Lock()
	l := fileWriteLocks.byPath[path]
	if l == nil {
		l = &fileWriteLock{}
		fileWriteLocks.byPath[path] = l
	}
	l.refs++
	fileWriteLocks.Unlock()

	l.mu.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Unlock()
			fileWriteLocks.Lock()
			l.refs--
			if l.refs <= 0 && fileWriteLocks.byPath[path] == l {
				delete(fileWriteLocks.byPath, path)
			}
			fileWriteLocks.Unlock()
		})
	}
}

const (
	leftSingleCurlyQuote  = "‘"
	rightSingleCurlyQuote = "’"
	leftDoubleCurlyQuote  = "“"
	rightDoubleCurlyQuote = "”"
)

func normalizeTextEditQuotes(s string) string {
	s = strings.ReplaceAll(s, leftSingleCurlyQuote, "'")
	s = strings.ReplaceAll(s, rightSingleCurlyQuote, "'")
	s = strings.ReplaceAll(s, leftDoubleCurlyQuote, `"`)
	s = strings.ReplaceAll(s, rightDoubleCurlyQuote, `"`)
	return s
}

func findActualEditString(fileContent, searchString string) string {
	if searchString == "" {
		return ""
	}
	if strings.Contains(fileContent, searchString) {
		return searchString
	}
	// Tolerate smart/curly quotes introduced by editors.
	if actual := findViaQuoteNormalization(fileContent, searchString); actual != "" {
		return actual
	}
	// Tolerate whitespace transcription differences (tabs vs spaces, trailing
	// whitespace, CRLF vs LF) that occur when the caller retransmits text it
	// read from a rendered view. Returns the exact on-disk substring so the
	// caller replaces the real bytes.
	if actual := findWhitespaceInsensitive(fileContent, searchString); actual != "" {
		return actual
	}
	return ""
}

// findViaQuoteNormalization is the curly/smart-quote tolerant matcher. Quote
// normalization is a 1:1 rune replacement, so normalized positions map directly
// back to original rune positions.
func findViaQuoteNormalization(fileContent, searchString string) string {
	normalizedSearch := normalizeTextEditQuotes(searchString)
	normalizedFile := normalizeTextEditQuotes(fileContent)
	byteIdx := strings.Index(normalizedFile, normalizedSearch)
	if byteIdx < 0 {
		return ""
	}
	startRune := len([]rune(normalizedFile[:byteIdx]))
	searchRunes := len([]rune(normalizedSearch))
	fileRunes := []rune(fileContent)
	if startRune < 0 || startRune+searchRunes > len(fileRunes) {
		return ""
	}
	return string(fileRunes[startRune : startRune+searchRunes])
}

// findWhitespaceInsensitive finds searchString within fileContent ignoring
// differences in runs of spaces/tabs and in line endings (CRLF vs LF). On match
// it returns the corresponding substring of the original fileContent so callers
// replace the exact on-disk bytes. It preserves line structure (newlines are
// not collapsed) to keep matches well-scoped.
func findWhitespaceInsensitive(fileContent, searchString string) string {
	if searchString == "" {
		return ""
	}
	normFile, origIndex := collapseWhitespaceWithIndex(fileContent)
	normSearch := collapseWhitespace(searchString)
	if normSearch == "" {
		return ""
	}
	idx := strings.Index(normFile, normSearch)
	if idx < 0 {
		return ""
	}
	startNorm := len([]rune(normFile[:idx]))
	endNorm := startNorm + len([]rune(normSearch))
	if startNorm >= len(origIndex) || endNorm > len(origIndex) {
		return ""
	}
	origStart := origIndex[startNorm]
	// The exclusive end is the start of the next normalized rune (so any
	// trailing whitespace run that was collapsed is included), or the end of
	// the file when the match reaches the last rune.
	origEnd := -1
	if endNorm < len(origIndex) {
		origEnd = origIndex[endNorm]
	}
	fileRunes := []rune(fileContent)
	if origEnd < 0 {
		origEnd = len(fileRunes)
	}
	if origStart < 0 || origEnd > len(fileRunes) || origStart >= origEnd {
		return ""
	}
	return string(fileRunes[origStart:origEnd])
}

// collapseWhitespaceWithIndex collapses runs of spaces and tabs into a single
// space and drops carriage returns (so CRLF matches LF). It returns the
// normalized string plus origIndex, where origIndex[i] is the index (in the
// collapseWhitespaceWithIndex produces a whitespace-normalized form of s for
// fuzzy matching, together with origIndex, where origIndex[i] is the index (in
// the original rune slice) of the rune that produced the i-th normalized rune.
// Normalization rules (applied to both file and search so they stay aligned):
//   - carriage returns are dropped, so CRLF matches LF;
//   - runs of spaces/tabs are collapsed to a single space;
//   - trailing whitespace on a line (spaces/tabs immediately before a newline
//
// collapseWhitespaceWithIndex produces a whitespace-normalized form of s for
// fuzzy matching, together with origIndex, where origIndex[i] is the index (in
// the original rune slice) of the rune that produced the i-th normalized rune.
// Normalization rules (applied to both file and search so they stay aligned):
//   - carriage returns are dropped, so CRLF matches LF;
//   - runs of spaces/tabs are collapsed to a single space;
//   - trailing whitespace on a line (spaces/tabs immediately before a newline
//     or end-of-file) is dropped, so "x   " matches "x".
//
// Newlines are preserved to keep matches line-scoped.
func collapseWhitespaceWithIndex(s string) (string, []int) {
	var b strings.Builder
	origIndex := make([]int, 0, len(s))
	runes := []rune(s)
	n := len(runes)
	i := 0
	for i < n {
		r := runes[i]
		if r == '\r' {
			i++
			continue
		}
		if r == ' ' || r == '\t' {
			// Extend over the full run of horizontal whitespace.
			j := i
			for j < n && (runes[j] == ' ' || runes[j] == '\t') {
				j++
			}
			// If the run runs to end-of-line (next is newline) or end-of-file,
			// drop it entirely (right-trim). Otherwise collapse to one space.
			if j >= n || runes[j] == '\n' {
				i = j
				continue
			}
			b.WriteByte(' ')
			origIndex = append(origIndex, i)
			i = j
			continue
		}
		b.WriteRune(r)
		origIndex = append(origIndex, i)
		i++
	}
	return b.String(), origIndex
}

// collapseWhitespace is the index-free variant used for the search string.
func collapseWhitespace(s string) string {
	norm, _ := collapseWhitespaceWithIndex(s)
	return norm
}

func preserveTextEditQuoteStyle(oldString, actualOldString, newString string) string {
	if oldString == actualOldString {
		return newString
	}
	hasDouble := strings.Contains(actualOldString, leftDoubleCurlyQuote) || strings.Contains(actualOldString, rightDoubleCurlyQuote)
	hasSingle := strings.Contains(actualOldString, leftSingleCurlyQuote) || strings.Contains(actualOldString, rightSingleCurlyQuote)
	out := newString
	if hasDouble {
		out = applyCurlyDoubleQuotes(out)
	}
	if hasSingle {
		out = applyCurlySingleQuotes(out)
	}
	return out
}

// preserveIndentationStyle re-indents newString so its leading whitespace
// matches the on-disk style (actualOldString) when the caller's oldString used
// a different indentation (e.g. spaces instead of tabs). It only adjusts
// leading whitespace per line, and only when:
//   - oldString and actualOldString have the same number of lines, and
//   - newString has the same number of lines, and
//   - the new line's leading whitespace equals the caller's old leading
//     whitespace (i.e. the caller preserved the original indentation rather
//     than intentionally changing it).
//
// This keeps Go files tab-indented and other files consistent with their
// existing style without clobbering deliberate indentation changes.
func preserveIndentationStyle(oldString, actualOldString, newString string) string {
	if oldString == actualOldString {
		return newString
	}
	oldLines := strings.Split(oldString, "\n")
	actualLines := strings.Split(actualOldString, "\n")
	newLines := strings.Split(newString, "\n")
	if len(oldLines) != len(actualLines) || len(newLines) != len(oldLines) {
		return newString
	}
	changed := false
	for i := range newLines {
		oldLead := leadingWhitespace(oldLines[i])
		actualLead := leadingWhitespace(actualLines[i])
		if oldLead == actualLead {
			continue
		}
		// Only re-indent when the new line kept the caller's original indentation.
		if leadingWhitespace(newLines[i]) != oldLead {
			continue
		}
		newLines[i] = actualLead + strings.TrimLeft(newLines[i], " \t")
		changed = true
	}
	if !changed {
		return newString
	}
	return strings.Join(newLines, "\n")
}

// leadingWhitespace returns the leading run of spaces and tabs of s.
func leadingWhitespace(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}

func applyCurlyDoubleQuotes(s string) string {
	runes := []rune(s)
	out := strings.Builder{}
	for i, r := range runes {
		if r == '"' {
			if isOpeningQuoteContext(runes, i) {
				out.WriteString(leftDoubleCurlyQuote)
			} else {
				out.WriteString(rightDoubleCurlyQuote)
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func applyCurlySingleQuotes(s string) string {
	runes := []rune(s)
	out := strings.Builder{}
	for i, r := range runes {
		if r == '\'' {
			prevIsLetter := i > 0 && isLetter(runes[i-1])
			nextIsLetter := i < len(runes)-1 && isLetter(runes[i+1])
			if prevIsLetter && nextIsLetter {
				out.WriteString(rightSingleCurlyQuote)
			} else if isOpeningQuoteContext(runes, i) {
				out.WriteString(leftSingleCurlyQuote)
			} else {
				out.WriteString(rightSingleCurlyQuote)
			}
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

func isOpeningQuoteContext(runes []rune, index int) bool {
	if index == 0 {
		return true
	}
	switch runes[index-1] {
	case ' ', '\t', '\n', '\r', '(', '[', '{', '—', '–':
		return true
	default:
		return false
	}
}

func isLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= 'À' && r <= 'ɏ')
}
