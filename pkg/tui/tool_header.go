package tui

import (
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// The wording of a tool card's header — "Read types.go lines 2-101", "Ran ls",
// "Notes" — is the engine's (tool.DisplayHeader), shared with the web: the
// terminal's words are the standard and every surface draws the same ones. This
// file only adapts the terminal's Frame to the engine's DisplayFrame.

// displayFrameOf is the engine's view of a tool frame.
func displayFrameOf(f Frame, summary string) tool.DisplayFrame {
	meta := f.ToolMeta
	if f.Kind != FrameTool {
		// A status belongs to tool frames only.
		meta.Status = ""
	}
	return tool.DisplayFrame{
		Title:    f.Title,
		Summary:  summary,
		Meta:     meta,
		Final:    f.Final,
		FilePath: f.FilePath,
		Content:  f.Content,
		Duration: f.Duration,
	}
}

// ToolDisplayHeader returns the Style A header text for a Frame, e.g. "Read types.go lines 2-101".
// Used by the viewport to build collapsed block headers.
func ToolDisplayHeader(f Frame, cwd string) string {
	return tool.DisplayHeader(displayFrameOf(f, strings.TrimSpace(f.Summary)), cwd)
}

// toolDisplayParts maps a Frame's tool title and summary to three user-facing
// parts: action (past/progressive verb), target (the thing acted on), and
// suffix (extra metadata like duration or exit status).
func toolDisplayParts(f Frame, summary string, cwd string) (action, target, suffix string) {
	return tool.DisplayParts(displayFrameOf(f, summary), cwd)
}

// skillCardStatus reports which Skill card a frame renders as: "loading",
// "completed" or "failed", and "" when the frame is not a Skill card at all.
func skillCardStatus(f Frame) string {
	if f.Kind != FrameTool {
		return ""
	}
	return tool.SkillCardStatus(displayFrameOf(f, ""))
}

func filePathFromToolMeta(tm tool.ToolMeta) string { return tool.FilePathFromToolMeta(tm) }

func inputString(tm tool.ToolMeta, key string) string { return tool.InputString(tm, key) }

func failedActionPhrase(toolName string) string { return tool.FailedActionPhrase(toolName) }

func formatDuration(d time.Duration) string { return tool.FormatDuration(d) }

func formatWorkingElapsed(elapsed time.Duration) string { return tool.FormatWorkingElapsed(elapsed) }

func displayPath(absPath, cwd string) string { return tool.DisplayPath(absPath, cwd) }

func isMemoryQueryTool(title string) bool { return tool.IsMemoryQueryTool(title) }

func singleDisplayLine(s string) string { return tool.SingleDisplayLine(s) }

func truncateForDisplay(s string, maxLen int) string { return tool.TruncateForDisplay(s, maxLen) }
