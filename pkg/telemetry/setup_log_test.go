package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/home"
)

// withEventLog installs one event log for a test and returns its home. Every
// record a helper writes goes there: the event log is the only destination
// these writers have left (plan 285).
func withEventLog(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	l := OpenEventLog(root, "test")
	prev := DefaultEventLog()
	SetDefaultEventLog(l)
	t.Cleanup(func() {
		SetDefaultEventLog(prev)
		_ = l.Close()
	})
	return root
}

// recordFiles lists the record files written for root today.
func recordFiles(t *testing.T, root string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, home.LogsDir, time.Now().Format("2006-01-02"), "*.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return files
}

// recordsRead reads every record written for root, oldest part first.
func recordsRead(t *testing.T, root string) []Record {
	t.Helper()
	var records []Record
	for _, path := range recordFiles(t, root) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if line == "" {
				continue
			}
			var rec Record
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("line is not a record: %v\n%s", err, line)
			}
			records = append(records, rec)
		}
	}
	return records
}

// onlyRecord is recordsRead for the tests that expect a single record.
func onlyRecord(t *testing.T, root string) Record {
	t.Helper()
	records := recordsRead(t, root)
	if len(records) != 1 {
		t.Fatalf("records = %+v, want exactly one", records)
	}
	return records[0]
}

// attrsTail is the text of the one record's attrs object, which is where both
// the keys and their order live: the decoded Attrs is a map and has neither.
func attrsTail(t *testing.T, root string) string {
	t.Helper()
	files := recordFiles(t, root)
	if len(files) != 1 {
		t.Fatalf("files = %v, want one", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	line := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0]
	start := strings.Index(line, `"attrs":`)
	if start < 0 {
		t.Fatalf("no attrs in the record: %s", line)
	}
	return line[start:]
}

func TestSetupEventRecordsAndRedactsSecrets(t *testing.T) {
	root := withEventLog(t)
	SetupEvent(root, "setup.start", map[string]any{
		"provider":    "openai",
		"api_key":     "sk-live-secret",
		"api_key_set": true,
		"base_url":    "http://10.20.200.100:30122/v1",
		"auth":        "Bearer raw-token",
	})

	rec := onlyRecord(t, root)
	if rec.Kind != "setup" || rec.Level != "INFO" || rec.Msg != "setup.start" {
		t.Fatalf("record = %+v, want an INFO setup record for setup.start", rec)
	}
	for key, want := range map[string]string{
		"provider":    "openai",
		"api_key":     "[REDACTED]",
		"api_key_set": "true",
		"base_url":    "http://10.20.200.100:30122/v1",
		"auth":        "Bearer [REDACTED]",
	} {
		if got := rec.Attrs[key]; got != want {
			t.Fatalf("%s = %v, want %q (record: %+v)", key, got, want, rec)
		}
	}
	encoded, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	for _, secret := range []string{"sk-live-secret", "raw-token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("the record leaked %q: %s", secret, encoded)
		}
	}
}

// The same fields twice must write the same attribute order: the map iteration
// this replaces reordered them on every run (plans/285 §1 point 9).
func TestSetupEventKeepsAttributeOrderStable(t *testing.T) {
	fields := map[string]any{"zeta": 1, "alpha": 2, "mike": 3, "beta": 4}

	firstRoot := withEventLog(t)
	SetupEvent(firstRoot, "setup.order", fields)
	first := attrsTail(t, firstRoot)

	secondRoot := withEventLog(t)
	SetupEvent(secondRoot, "setup.order", fields)
	second := attrsTail(t, secondRoot)

	if first != second {
		t.Fatalf("two calls ordered the same fields differently:\n%s\n%s", first, second)
	}
	at := func(key string) int { return strings.Index(first, `"`+key+`":`) }
	if !(at("alpha") < at("beta") && at("beta") < at("mike") && at("mike") < at("zeta")) {
		t.Fatalf("attributes are not sorted by key: %s", first)
	}
}

// A setup call with no log configured writes nothing and creates no file.
func TestSetupEventWithNoLogIsSilent(t *testing.T) {
	prev := DefaultEventLog()
	SetDefaultEventLog(nil)
	t.Cleanup(func() { SetDefaultEventLog(prev) })

	root := t.TempDir()
	SetupEvent(root, "setup.none", map[string]any{"provider": "openai"})
	if entries, err := os.ReadDir(filepath.Join(root, home.LogsDir)); err == nil && len(entries) > 0 {
		t.Fatalf("a setup event with no log wrote %v", entries)
	}
}

// A blank event name is still a name a reader can grep for.
func TestSetupEventNamesABlankEvent(t *testing.T) {
	root := withEventLog(t)
	SetupEvent(root, "  ", nil)
	if rec := onlyRecord(t, root); rec.Msg != "unknown" || rec.Kind != "setup" {
		t.Fatalf("record = %+v, want an unknown setup event", rec)
	}
}
