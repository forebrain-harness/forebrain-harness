package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

var (
	debugFileLogMu   sync.Mutex
	debugFileLogFile *File
	debugFileLogOnce sync.Once
	rootDirFn        = home.Root
)

func debugFileLogEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("FOREBRAIN_LLM_HTTP_DEBUG")))
	if v == "0" || v == "false" || v == "no" || v == "off" {
		return false
	}
	return true
}

// withDebugFileLog opens debug.log lazily once, then runs write under the
// process-wide debug log lock. It is the shared body of writeDebugFileLog and
// writeDebugFileLogWhole so the open path cannot drift between them.
func withDebugFileLog(write func(*File)) {
	if !debugFileLogEnabled() {
		return
	}
	debugFileLogOnce.Do(func() {
		root, err := rootDirFn()
		if err != nil {
			return
		}
		p := filepath.Join(root, home.LogsDir, "debug.log")
		f, err := Open(p, Options{ExistingParentOnly: true})
		if err == nil {
			debugFileLogFile = f
		}
	})
	debugFileLogMu.Lock()
	defer debugFileLogMu.Unlock()
	if debugFileLogFile != nil {
		write(debugFileLogFile)
	}
}

func writeDebugFileLog(section, msg string) {
	withDebugFileLog(func(f *File) {
		_ = f.WriteSection(section, msg)
	})
}

// writeDebugFileLogWhole writes the block as given: the caller has already
// bounded whatever needed bounding, or deliberately bounded nothing (a
// model's output, which debug.log must record in full).
func writeDebugFileLogWhole(section, msg string) {
	withDebugFileLog(func(f *File) {
		_ = f.WriteSectionWhole(section, msg)
	})
}

// LogProviderDebug records one provider debug line under topic. It replaces a
// per-call-site helper (LogOpenAIResponsesContentPreview) so that provider
// clients, which may not import this package, can emit debug output through
// llm's port instead.
func LogProviderDebug(topic, message string) {
	if !debugFileLogEnabled() {
		return
	}
	writeDebugFileLog(topic, message)
}
