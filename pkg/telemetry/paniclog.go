package telemetry

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

var writeMu sync.Mutex

func Log(forebrainRoot string, label string, r any) {
	if r == nil {
		return
	}
	root := strings.TrimSpace(forebrainRoot)
	if root == "" {
		x, err := datadirRoot()
		if err != nil {
			return
		}
		root = x
	}
	path := filepath.Join(root, home.LogsDir, "error.log")
	pc, f, l, ok := runtimeCaller(2)
	if !ok {
		f, l = "?", 0
		pc = 0
	}
	_ = appendFile(path, label, r, f, l, functionNameForPC(pc))
}

func appendFile(path string, label string, r any, reportFile string, reportLine int, reportFn string) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	var b strings.Builder
	fmt.Fprintf(&b, "=== [PANIC-RECOVER] %s %s\n", time.Now().Format(time.RFC3339Nano), label)
	fmt.Fprintf(&b, "location: %s:%d\n", reportFile, reportLine)
	fmt.Fprintf(&b, "function: %s\n", reportFn)
	b.WriteString(formatRecovered(r))
	b.WriteString("debug.Stack():\n")
	b.Write(debug.Stack())
	b.WriteString("\n\n")
	_, err = f.WriteString(b.String())
	return err
}

func formatRecovered(r any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%%v: %v\n", r)
	fmt.Fprintf(&b, "%%#v: %#v\n", r)
	if err, ok := r.(error); ok {
		fmt.Fprintf(&b, "Error(): %s\n", err.Error())
	}
	return b.String()
}
