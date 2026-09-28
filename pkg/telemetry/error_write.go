package telemetry

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

var (
	runtimeCaller = runtime.Caller
	datadirRoot   = home.Root
)

func ErrorSkip(forebrainRoot string, err error, context string, extraFrames int) {
	if err == nil {
		return
	}
	if extraFrames < 0 {
		extraFrames = 0
	}
	skip := 2 + extraFrames
	pc, file, line, ok := runtimeCaller(skip)
	if !ok {
		file, line = "?", 0
		pc = 0
	}
	_ = writeErrorEntry(forebrainRoot, err, context, file, line, functionNameForPC(pc))
}

func ErrorThenFprintln(forebrainRoot string, w io.Writer, err error, context string) {
	if err == nil {
		return
	}
	ErrorSkip(forebrainRoot, err, context, 1)
	_, _ = fmt.Fprintln(w, err)
}

func functionNameForPC(pc uintptr) string {
	if pc == 0 {
		return "?"
	}
	if fp := runtime.FuncForPC(pc); fp != nil {
		return fp.Name()
	}
	return "?"
}

func writeErrorEntry(forebrainRoot string, err error, context string, file string, line int, function string) error {
	if err == nil {
		return nil
	}
	root := resolveRoot(forebrainRoot)
	path := filepath.Join(root, home.LogsDir, "error.log")
	return appendErrorFile(path, err, context, file, line, function)
}

func resolveRoot(forebrainRoot string) string {
	root := strings.TrimSpace(forebrainRoot)
	if root != "" {
		return root
	}
	x, e := datadirRoot()
	if e != nil {
		return ""
	}
	return x
}

func formatErrorDetails(err error) string {
	if err == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%%v: %v\n", err)
	fmt.Fprintf(&b, "%%#v: %#v\n", err)
	fmt.Fprintf(&b, "Error(): %s\n", err.Error())
	i := 0
	for u := errors.Unwrap(err); u != nil && i < 32; u = errors.Unwrap(u) {
		fmt.Fprintf(&b, "unwrap[%d]: %v\n", i+1, u)
		i++
	}
	return b.String()
}

func appendErrorFile(path string, err error, context string, file string, line int, function string) error {
	if err == nil {
		return nil
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	if e := os.MkdirAll(filepath.Dir(path), 0o755); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if e != nil {
		return e
	}
	defer f.Close()
	var b strings.Builder
	fmt.Fprintf(&b, "=== [ERROR] %s\n", time.Now().Format(time.RFC3339Nano))
	if context != "" {
		fmt.Fprintf(&b, "context: %s\n", context)
	}
	fmt.Fprintf(&b, "location: %s:%d\n", file, line)
	fmt.Fprintf(&b, "function: %s\n", function)
	b.WriteString(formatErrorDetails(err))
	b.WriteString("---\n\n")
	_, e = f.WriteString(b.String())
	return e
}
