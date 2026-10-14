package mcp

import (
	"reflect"
	"strings"
	"testing"
)

// TestStdioTargetRoundTripsEveryArgument pins the property the settings field
// depends on: rendering a command and its arguments as one line and parsing it
// back returns exactly what went in. An argument with a space in it is one
// argument, not two.
func TestStdioTargetRoundTripsEveryArgument(t *testing.T) {
	cases := []struct {
		command string
		args    []string
	}{
		{"mcp-server", []string{"--root", "/tmp/a b"}},
		{"npx", []string{"-y", "smart-doc-mcp", "--stdio"}},
		{"/usr/local/bin/docker", []string{"run", "-i", "--rm", "img:tag"}},
		{"python3", []string{"scripts/acceptance/fake_mcp_stdio.py", "echo", "name with 'quotes'"}},
		{"tool", []string{`say "hi"`, `back\slash`, ""}},
		{"cmd", []string{"-Dkey=value with spaces", "--flag"}},
		{"only-command", nil},
	}
	for _, tc := range cases {
		rendered := FormatStdioTarget(tc.command, tc.args)
		command, args, err := SplitStdioTarget(rendered)
		if err != nil {
			t.Fatalf("split %q: %v", rendered, err)
		}
		if command != tc.command {
			t.Fatalf("command = %q, want %q (from %q)", command, tc.command, rendered)
		}
		if len(tc.args) == 0 {
			if len(args) != 0 {
				t.Fatalf("args = %v, want none (from %q)", args, rendered)
			}
			continue
		}
		if !reflect.DeepEqual(args, tc.args) {
			t.Fatalf("args = %v, want %v (from %q)", args, tc.args, rendered)
		}
	}
}

// TestFormatStdioTargetQuotesOnlyWhenItMust keeps a plain command readable: the
// line a user sees for `npx -y smart-doc-mcp` is that, not a quoted form.
func TestFormatStdioTargetQuotesOnlyWhenItMust(t *testing.T) {
	if got := FormatStdioTarget("npx", []string{"-y", "smart-doc-mcp"}); got != "npx -y smart-doc-mcp" {
		t.Fatalf("rendered = %q", got)
	}
	if got := FormatStdioTarget("mcp-server", []string{"--root", "/tmp/a b"}); got != `mcp-server --root "/tmp/a b"` {
		t.Fatalf("rendered = %q", got)
	}
}

// TestSplitStdioTargetParsesHandWrittenLines pins the other direction: a line
// typed by hand, with quotes the user wrote, means what it says.
func TestSplitStdioTargetParsesHandWrittenLines(t *testing.T) {
	command, args, err := SplitStdioTarget(`mcp-server --root "/tmp/a b" 'single quoted' plain`)
	if err != nil {
		t.Fatal(err)
	}
	if command != "mcp-server" {
		t.Fatalf("command = %q", command)
	}
	want := []string{"--root", "/tmp/a b", "single quoted", "plain"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

// TestSplitStdioTargetRejectsAnUnclosedQuote pins that a half-typed line is
// refused rather than silently parsed into something else — the user is looking
// at the field when it happens.
func TestSplitStdioTargetRejectsAnUnclosedQuote(t *testing.T) {
	_, _, err := SplitStdioTarget(`mcp-server --root "/tmp/a b`)
	if err == nil {
		t.Fatal("an unclosed quote must be an error")
	}
	if !strings.Contains(err.Error(), "unclosed") {
		t.Fatalf("error = %v", err)
	}
}

// TestSplitStdioTargetEmptyIsNoCommand keeps the empty field empty: it is what a
// cleared form submits.
func TestSplitStdioTargetEmptyIsNoCommand(t *testing.T) {
	command, args, err := SplitStdioTarget("   ")
	if err != nil {
		t.Fatal(err)
	}
	if command != "" || len(args) != 0 {
		t.Fatalf("command=%q args=%v", command, args)
	}
}
