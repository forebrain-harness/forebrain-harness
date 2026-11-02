package peer

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// The values this package writes into a record. Claude Code only understands
// these, so nothing else may appear: an unknown kind or status reads back as
// "no such field" on the other side.
const (
	peerProtocolVersion = 1
	kindInteractive     = "interactive"
	nameSourceDerived   = "derived"

	StatusBusy    = "busy"
	StatusShell   = "shell"
	StatusIdle    = "idle"
	StatusWaiting = "waiting"
)

// maxRecordSize is the size past which a record is not ours to interpret (the
// same cap Claude Code reads with).
const maxRecordSize = 262144

// Record is one entry of the rendezvous directory, <pid>.json. The JSON names
// are Claude Code's, and every field it writes is here so a record we write is
// indistinguishable in shape from one of its own.
type Record struct {
	PID                 int      `json:"pid"`
	SessionID           string   `json:"sessionId"`
	Cwd                 string   `json:"cwd"`
	StartedAt           int64    `json:"startedAt"`
	ProcStart           string   `json:"procStart"`
	Version             string   `json:"version"`
	PeerProtocol        int      `json:"peerProtocol"`
	PeerFeatures        []string `json:"peerFeatures"`
	Kind                string   `json:"kind"`
	Entrypoint          string   `json:"entrypoint"`
	PIDDomain           string   `json:"pidDomain"`
	MessagingSocketPath string   `json:"messagingSocketPath"`
	Name                string   `json:"name"`
	NameSource          string   `json:"nameSource"`
	NameSince           int64    `json:"nameSince"`
	Status              string   `json:"status"`
	UpdatedAt           int64    `json:"updatedAt"`
	StatusUpdatedAt     int64    `json:"statusUpdatedAt"`

	// Forebrain is the extension block Claude Code ignores. It carries the
	// agents this process hosts, which is how forebrain addresses an agent
	// rather than a process.
	Forebrain *Extension `json:"forebrain,omitempty"`

	// The rest is read, never written by this package: formerNames and jobId
	// are informational, spare and parkedJobId mark a record that is not a
	// messageable session (a parked job or a spare), which listing skips.
	FormerNames []string `json:"formerNames,omitempty"`
	Spare       bool     `json:"spare,omitempty"`
	ParkedJobID string   `json:"parkedJobId,omitempty"`
	JobID       string   `json:"jobId,omitempty"`
}

// Extension is forebrain's block inside a record. Version 1 is the only shape
// this package writes; the agents are the addressable units behind the process.
type Extension struct {
	V       int        `json:"v"`
	Surface string     `json:"surface"`
	Agents  []ExtAgent `json:"agents"`
}

// ExtAgent is one addressable agent of a process: the session's primary agent
// or one of its subagents.
type ExtAgent struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Parent string `json:"parent,omitempty"`
	Root   string `json:"root"`
	Branch string `json:"branch,omitempty"`
	Status string `json:"status"`
	Task   string `json:"task,omitempty"`
	Mode   string `json:"mode"`
	Since  int64  `json:"since"`
}

// WriteRecord writes a record as <pid>.json in dir, atomically. The directory
// is never created here: it belongs to the layout, and Claude Code's directory
// in particular must never be created by us — a missing one is an error the
// caller reports.
func WriteRecord(dir string, r Record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeFileAtomic(dir, strconv.Itoa(r.PID)+".json", data, 0o644)
}

// ReadRecords reads every record dir holds. Files that are not
// <canonical pid>.json, are larger than any record can be, or do not parse are
// skipped: the directory is shared, and one bad entry is not our business.
//
// Records marked spare or parked are left out, exactly as Claude Code leaves
// them out of its own listing: they are not sessions anything can message.
func ReadRecords(dir string) []Record {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		pid, ok := recordPID(entry.Name())
		if !ok || entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		info, err := os.Stat(path)
		if err != nil || info.Size() > maxRecordSize {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var record Record
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}
		if record.PID == 0 {
			record.PID = pid
		}
		if record.Spare || record.ParkedJobID != "" {
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].PID < records[j].PID })
	return records
}

// recordNamePattern is Claude Code's reader pattern; the pid in it must also be
// the canonical decimal, so "012.json" names nothing.
var recordNamePattern = regexp.MustCompile(`^(\d+)\.json$`)

// recordPID reads the pid out of a record file name.
func recordPID(name string) (int, bool) {
	match := recordNamePattern.FindStringSubmatch(name)
	if match == nil {
		return 0, false
	}
	pid, err := strconv.Atoi(match[1])
	if err != nil || pid <= 0 || strconv.Itoa(pid) != match[1] {
		return 0, false
	}
	return pid, true
}

// writeFileAtomic replaces dir/name with data in one rename, so a reader never
// sees a half-written file: the temporary is a dot file with eight random hex
// digits, exactly the shape Claude Code's own writes leave behind.
func writeFileAtomic(dir, name string, data []byte, perm os.FileMode) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		suffix, err := randomHex(4)
		if err != nil {
			return err
		}
		tmp := filepath.Join(dir, "."+name+".tmp."+suffix)
		file, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
		if err != nil {
			lastErr = err
			continue
		}
		if _, err := file.Write(data); err != nil {
			file.Close()
			os.Remove(tmp)
			return err
		}
		if err := file.Close(); err != nil {
			os.Remove(tmp)
			return err
		}
		if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
			os.Remove(tmp)
			return err
		}
		return nil
	}
	return lastErr
}

// randomHex is n random bytes as lowercase hex.
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
