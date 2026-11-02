package peer

import (
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// procStartShape is the exact shape of `ps -o lstart=` in the C locale.
var procStartShape = regexp.MustCompile(`^[A-Z][a-z]{2} [A-Z][a-z]{2} [ 0-9][0-9] [0-9]{2}:[0-9]{2}:[0-9]{2} [0-9]{4}$`)

// psStart is what Claude Code itself would record for pid: the trimmed output
// of `LC_ALL=C TZ=UTC ps -o lstart= -p <pid>`. ps appears in this test only, to
// prove the in-process reading agrees with it byte for byte; the production
// code never runs a command.
func psStart(t *testing.T, pid int) string {
	t.Helper()
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ps -p %d: %v", pid, err)
	}
	return strings.TrimSpace(string(out))
}

func TestProcStartMatchesPS(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no process start time on this platform")
	}
	for _, pid := range []int{os.Getpid(), 1} {
		got, ok := ProcStart(pid)
		if !ok {
			t.Fatalf("ProcStart(%d) found nothing", pid)
		}
		if !procStartShape.MatchString(got) {
			t.Fatalf("ProcStart(%d) = %q, which does not match the ps shape", pid, got)
		}
		if want := psStart(t, pid); got != want {
			t.Fatalf("ProcStart(%d) = %q, ps says %q", pid, got, want)
		}
	}

	if _, ok := ProcStart(999999); ok {
		t.Fatal("ProcStart found a start time for a pid that does not exist")
	}
}

func TestPIDDomain(t *testing.T) {
	domain := PIDDomain()
	switch runtime.GOOS {
	case "darwin":
		if domain != "darwin" {
			t.Fatalf("PIDDomain = %q, want darwin", domain)
		}
	case "linux":
		if !strings.HasPrefix(domain, "darwin:") || strings.Count(domain, ":") != 2 {
			t.Fatalf("PIDDomain = %q, want the darwin:<machine-id>:<ns> form", domain)
		}
	default:
		if domain != "" {
			t.Fatalf("PIDDomain = %q on an unsupported platform", domain)
		}
	}
	if again := PIDDomain(); again != domain {
		t.Fatalf("PIDDomain changed between calls: %q vs %q", again, domain)
	}
}

func TestAlive(t *testing.T) {
	self := os.Getpid()
	start, ok := ProcStart(self)
	if !ok {
		t.Fatalf("ProcStart(%d) found nothing", self)
	}

	live := Record{PID: self, ProcStart: start, PIDDomain: PIDDomain()}
	if !Alive(live) {
		t.Fatal("our own record is not alive")
	}

	// A different pid domain is taken at its word: its pid says nothing about
	// ours.
	if !Alive(Record{PID: 999999, PIDDomain: "darwin:other-machine:pid:[1]"}) {
		t.Fatal("a record from another pid domain was judged dead")
	}

	// The pid exists but its start time no longer matches: the pid was reused.
	if Alive(Record{PID: self, ProcStart: "Fri Oct 30 06:47:30 2026", PIDDomain: PIDDomain()}) {
		t.Fatal("a record with a stale start time is alive")
	}

	// A pid that is gone.
	dead := deadPID(t)
	if Alive(Record{PID: dead, ProcStart: start, PIDDomain: PIDDomain()}) {
		t.Fatalf("pid %d is alive after it exited", dead)
	}
	if _, ok := ProcStart(dead); ok {
		t.Fatalf("ProcStart(%d) found a start time for a pid that exited", dead)
	}

	// A recorded socket that nothing listens on.
	if Alive(Record{PID: self, ProcStart: start, PIDDomain: PIDDomain(), MessagingSocketPath: "/tmp/fbp-no-such-socket.sock"}) {
		t.Fatal("a record with a dead socket is alive")
	}
}

// deadPID runs a short-lived child and returns the pid once it has exited.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("sleep 0: %v", err)
	}
	return cmd.Process.Pid
}
