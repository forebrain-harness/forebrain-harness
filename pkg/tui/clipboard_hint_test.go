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
	b := &viewBlock{frame: f, runStart: time.Now().Add(-75 * time.Second)}
	if got := ToolDisplayHeader(b.displayFrame(), ""); !strings.Contains(got, "1m 15s") {
		t.Fatalf("running header lacks elapsed time: %q", got)
	}
	b.runStart = time.Now()
	if got := ToolDisplayHeader(b.displayFrame(), ""); strings.Contains(got, "0s") {
		t.Fatalf("sub-second run shows elapsed: %q", got)
	}
}
