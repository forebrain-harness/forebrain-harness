package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// TestOpenRefusesASharedSQLiteDatabaseFromAnotherHost pins the gateway's
// startup refusal (plan 213 §3.5): a gateway whose state database is SQLite
// and whose fb_run_owners already holds a live gateway registered on another
// host must not start, because SQLite on shared storage is not safe for two
// writers. A terminal opens regardless — it shares SQLite with the gateway on
// its own machine, as it always has.
func TestOpenRefusesASharedSQLiteDatabaseFromAnotherHost(t *testing.T) {
	t.Setenv("FOREBRAIN_COMPOSITION_TEST_KEY", "test-key")
	home := t.TempDir()
	configPath := filepath.Join(home, "forebrain.yaml")
	if err := os.WriteFile(configPath, []byte(compositionConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	launchDir := t.TempDir()
	gatewayOpts := func() OpenOptions {
		return OpenOptions{
			Home:          home,
			ConfigPath:    configPath,
			LaunchDir:     launchDir,
			EnableUploads: true,
			EnableChannels: true,
			SessionSource: "webchat",
			ReplicaKind:   state.RunOwnerKindGateway,
		}
	}

	first, err := Open(context.Background(), gatewayOpts())
	if err != nil {
		t.Fatalf("the first gateway on a host must open: %v", err)
	}
	resetProcessWideInstalls(t)
	defer first.Close()
	if dbx.DialectOf(first.SQL) != dbx.SQLite {
		t.Skip("the refusal is SQLite's alone; the servers are the multi-replica engines")
	}

	// A second host's gateway is already live in the same database.
	other := "forebrain-other-host"
	if self, err := os.Hostname(); err == nil && self == other {
		other = "forebrain-yet-another-host"
	}
	if _, err := first.SQL.Exec(dbx.Rebind(dbx.DialectOf(first.SQL),
		`INSERT INTO fb_run_owners(owner, heartbeat_at_ms, internal_addr, host, kind) VALUES(?,?,?,?,?)`),
		"webchat-other", time.Now().UnixMilli(), "http://10.0.3.99:8787", other, state.RunOwnerKindGateway); err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), gatewayOpts())
	if err == nil {
		second.Close()
		t.Fatal("a gateway on another host must not open over a shared SQLite database")
	}
	if !errors.Is(err, state.ErrSQLiteMultiHost) {
		t.Fatalf("a gateway on another host = %v, want ErrSQLiteMultiHost", err)
	}

	// A terminal beside that gateway is the ordinary single-machine setup.
	terminal, err := Open(context.Background(), OpenOptions{
		Home:          home,
		ConfigPath:    configPath,
		LaunchDir:     launchDir,
		SessionSource: "tui",
		ReplicaKind:   state.RunOwnerKindTerminal,
	})
	if err != nil {
		t.Fatalf("a terminal must still open beside a gateway: %v", err)
	}
	resetProcessWideInstalls(t)
	defer terminal.Close()
}
