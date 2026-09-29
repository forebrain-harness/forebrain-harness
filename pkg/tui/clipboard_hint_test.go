package tui

import (
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestClipboardImageHintLine(t *testing.T) {
	r := NewRenderer(nil, nil)
	if got := r.clipboardImageHintLine(80); got != "" {
		t.Fatalf("hint shown without an image: %q", got)
	}
	r.clipboardImage = true
	got := r.clipboardImageHintLine(80)
	if !strings.Contains(got, clipboardImageHintText) {
		t.Fatalf("hint missing: %q", got)
	}
	if w := runewidth.StringWidth(stripANSIForTest(got)); w != 80-sharedBlockFooterTruncateExtraRoom {
		t.Fatalf("hint not right-aligned, width %d", w)
	}
	if got := r.clipboardImageHintLine(5); strings.Contains(got, "ctrl+v to paste") {
		t.Fatalf("hint not truncated: %q", got)
	}
}

func stripANSIForTest(s string) string {
	var b strings.Builder
	esc := false
	for _, c := range s {
		switch {
		case c == 0x1b:
			esc = true
		case esc && c == 'm':
			esc = false
		case !esc:
			b.WriteRune(c)
		}
	}
	return b.String()
}

func TestRunningToolHeaderShowsElapsed(t *testing.T) {
	f := Frame{Kind: FrameTool, Title: "shell", Summary: "node x.mjs", ToolMeta: tool.ToolMeta{Status: "running"}}
	f.ToolMeta.StartedAtMs = time.Now().Add(-75 * time.Second).UnixMilli()
	b := &viewBlock{frame: f}
	if got := ToolDisplayHeader(b.displayFrame(), ""); !strings.Contains(got, "1m 15s") {
		t.Fatalf("running header lacks elapsed time: %q", got)
	}
	b.frame.ToolMeta.StartedAtMs = time.Now().UnixMilli()
	if got := ToolDisplayHeader(b.displayFrame(), ""); strings.Contains(got, "0s") {
		t.Fatalf("sub-second run shows elapsed: %q", got)
	}
}

func TestToolOutputBlockKeepsOneForegroundDespiteCommandColours(t *testing.T) {
	out := formatToolOutputBlock("plain \x1b[0mafter reset\n\x1b[97mbright white\x1b[39m tail")
	for _, seq := range []string{"\x1b[0mafter", "\x1b[97m", "\x1b[39m"} {
		if strings.Contains(out, seq) {
			t.Fatalf("command colour %q survived into the output block: %q", seq, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, thinkingColor) {
			t.Fatalf("row not painted in the output colour: %q", line)
		}
	}
}

func TestSlashComposerSoftWrapsLongArgument(t *testing.T) {
	r := NewRenderer(nil, nil)
	const width = 60
	text := "/improve plan forebrain有一个问题需要解决：通过slash命令/model或者/connect命令修改当前生效的primary agent的模型后，FOREBRAIN_HOME/forebrain.yaml配置文件里的llm"
	cursor := len([]rune(text))
	block := r.buildComposerBlock(ComposerRenderState{Text: text, Cursor: &cursor}, width)
	rows := 0
	for _, line := range block.lines {
		if w := runewidth.StringWidth(stripANSIForTest(line)); w >= width {
			t.Fatalf("row is %d cells wide on a %d-cell terminal, the terminal would autowrap it: %q", w, width, line)
		}
		if strings.Contains(line, "improve") || strings.Contains(line, "llm") {
			rows++
		}
	}
	if rows < 2 {
		t.Fatalf("long slash text was not wrapped across rows: %q", block.lines)
	}
	last := block.lines[block.cursorRow]
	if !strings.Contains(last, "llm") {
		t.Fatalf("caret row %d is not the last text row: %q", block.cursorRow, last)
	}
}
