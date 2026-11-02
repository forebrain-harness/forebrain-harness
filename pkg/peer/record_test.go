package peer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	record := Record{
		PID:                 4242,
		SessionID:           "session-1",
		Cwd:                 "/Users/u/repo",
		StartedAt:           1793342857359,
		ProcStart:           "Fri Oct 30 06:47:30 2026",
		Version:             "forebrain 0.0.0",
		PeerProtocol:        peerProtocolVersion,
		PeerFeatures:        []string{replyAcrossDefaultDirs},
		Kind:                kindInteractive,
		Entrypoint:          "forebrain-tui",
		PIDDomain:           PIDDomain(),
		MessagingSocketPath: "/tmp/fb-socks-501/4242.sock",
		Name:                "repo-28",
		NameSource:          nameSourceDerived,
		NameSince:           1793342857359,
		Status:              StatusBusy,
		UpdatedAt:           1793344027704,
		StatusUpdatedAt:     1793344027704,
		Forebrain: &Extension{
			V:       1,
			Surface: "tui",
			Agents: []ExtAgent{{
				ID:     "session-1",
				Name:   "repo-28",
				Kind:   "primary",
				Root:   "/Users/u/repo",
				Branch: "dev",
				Status: StatusBusy,
				Task:   "fix the thing",
				Mode:   "prompting",
				Since:  1793342857359,
			}},
		},
	}
	if err := WriteRecord(dir, record); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}

	path := filepath.Join(dir, "4242.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("record mode = %v, want 0644", info.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the directory holds %d entries, want the record alone", len(entries))
	}

	records := ReadRecords(dir)
	if len(records) != 1 {
		t.Fatalf("ReadRecords returned %d records", len(records))
	}
	got := records[0]
	if got.PID != record.PID || got.SessionID != record.SessionID || got.Name != record.Name {
		t.Fatalf("identity did not survive: %#v", got)
	}
	if got.MessagingSocketPath != record.MessagingSocketPath || got.PIDDomain != record.PIDDomain {
		t.Fatalf("addressing did not survive: %#v", got)
	}
	if got.Forebrain == nil || len(got.Forebrain.Agents) != 1 {
		t.Fatalf("the extension block did not survive: %#v", got.Forebrain)
	}
	agent := got.Forebrain.Agents[0]
	if agent.ID != "session-1" || agent.Branch != "dev" || agent.Task != "fix the thing" || agent.Mode != "prompting" {
		t.Fatalf("the agent did not survive: %#v", agent)
	}
}

func TestReadRecordsSkipsWhatIsNotOurs(t *testing.T) {
	dir := t.TempDir()
	good := readGolden(t, "record.json")
	if err := os.WriteFile(filepath.Join(dir, "49280.json"), good, 0o644); err != nil {
		t.Fatal(err)
	}
	// A non-canonical pid, a file that is not a record, an oversized record, a
	// record that does not parse, and two records that are not sessions.
	files := map[string]string{
		"012.json":  `{"pid":12}`,
		"notes.txt": `not a record`,
		"0.json":    `{"pid":0}`,
		"1.json":    `{"pid":1,` + strings.Repeat(" ", maxRecordSize),
		"2.json":    `{"pid":2,"sessionId":`,
		"3.json":    `{"pid":3,"spare":true}`,
		"4.json":    `{"pid":4,"parkedJobId":"job-1"}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "5.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	records := ReadRecords(dir)
	if len(records) != 1 || records[0].PID != 49280 {
		t.Fatalf("ReadRecords returned %#v, want only the real record", records)
	}
}

func TestReadRecordsReadsTheClaudeCodeGolden(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "49280.json"), readGolden(t, "record.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	records := ReadRecords(dir)
	if len(records) != 1 {
		t.Fatalf("ReadRecords returned %d records", len(records))
	}
	record := records[0]
	if record.PID != 49280 || record.SessionID != "4693bfa1-4681-4d4d-bac8-d2800c5acf11" {
		t.Fatalf("identity = %#v", record)
	}
	if record.Cwd != "/Users/u/repo" || record.StartedAt != 1793342857359 {
		t.Fatalf("working directory = %#v", record)
	}
	if record.ProcStart != "Fri Oct 30 06:47:30 2026" || record.Version != "2.1.301" {
		t.Fatalf("process identity = %#v", record)
	}
	if record.PeerProtocol != 1 || record.Kind != kindInteractive || record.Entrypoint != "cli" {
		t.Fatalf("protocol fields = %#v", record)
	}
	if len(record.PeerFeatures) != 3 || record.PeerFeatures[0] != "notify_idle" {
		t.Fatalf("features = %#v", record.PeerFeatures)
	}
	if record.PIDDomain != "darwin" || record.MessagingSocketPath != "/tmp/cc-socks/49280.sock" {
		t.Fatalf("addressing = %#v", record)
	}
	if record.Name != "repo-28" || record.NameSource != nameSourceDerived || record.NameSince != 1793342857359 {
		t.Fatalf("name = %#v", record)
	}
	if record.Status != StatusBusy || record.UpdatedAt != 1793344027704 || record.StatusUpdatedAt != 1793344027704 {
		t.Fatalf("status = %#v", record)
	}
	if record.Forebrain != nil {
		t.Fatalf("a Claude Code record grew an extension block: %#v", record.Forebrain)
	}
}

func TestReadRecordsOfAMissingDirectory(t *testing.T) {
	if records := ReadRecords(filepath.Join(t.TempDir(), "absent")); records != nil {
		t.Fatalf("ReadRecords of a missing directory = %#v", records)
	}
}
