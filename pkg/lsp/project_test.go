package lsp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
)

// writeProjectLSP puts a project file under root, creating .forebrain.
func writeProjectLSP(t *testing.T, root, body string) string {
	t.Helper()
	p := ProjectLSPPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadProjectServers(t *testing.T) {
	t.Run("missing file is no entries and no notes", func(t *testing.T) {
		m, notes := LoadProjectServers(t.TempDir())
		if len(m) != 0 || len(notes) != 0 {
			t.Fatalf("m=%v notes=%v", m, notes)
		}
	})
	t.Run("normal file with unknown top-level keys", func(t *testing.T) {
		root := t.TempDir()
		writeProjectLSP(t, root, "servers:\n  gopls:\n    args: [\"-tags=integration\"]\nextras: 1\n")
		m, notes := LoadProjectServers(root)
		if len(m) != 1 {
			t.Fatalf("m=%v", m)
		}
		if m["gopls"].Args[0] != "-tags=integration" {
			t.Fatalf("args=%v", m["gopls"].Args)
		}
		if len(notes) != 1 || notes[0] != `lsp_servers.yaml: unknown key "extras"` {
			t.Fatalf("notes=%q", notes)
		}
	})
	t.Run("identifiers are normalized", func(t *testing.T) {
		root := t.TempDir()
		writeProjectLSP(t, root, "servers:\n  GoPls:\n    extension_to_language: {\".GO\": go}\n")
		m, notes := LoadProjectServers(root)
		if len(notes) != 0 {
			t.Fatalf("notes=%q", notes)
		}
		srv, ok := m["gopls"]
		if !ok {
			t.Fatalf("id not lowercased: %v", m)
		}
		if _, ok := srv.ExtensionToLanguage[".go"]; !ok {
			t.Fatalf("extension key not lowercased: %v", srv.ExtensionToLanguage)
		}
	})
	t.Run("plaintext secret is rejected verbatim", func(t *testing.T) {
		root := t.TempDir()
		writeProjectLSP(t, root, "servers:\n  s:\n    command: x\n    env: {api_key: abc123}\n")
		m, notes := LoadProjectServers(root)
		if len(m) != 0 {
			t.Fatalf("m=%v", m)
		}
		want := "lsp_servers.yaml contains a plaintext secret; use ${ENV_NAME} resolved from ~/.forebrain/.env"
		if len(notes) != 1 || notes[0] != want {
			t.Fatalf("notes=%q", notes)
		}
	})
	t.Run("invalid id drops every entry with the validation text", func(t *testing.T) {
		root := t.TempDir()
		writeProjectLSP(t, root, "servers:\n  \"bad id\":\n    command: x\n")
		m, notes := LoadProjectServers(root)
		if len(m) != 0 {
			t.Fatalf("m=%v", m)
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "lsp_servers.yaml servers.bad id: server id must match") {
			t.Fatalf("notes=%q", notes)
		}
	})
	t.Run("env_passthrough and priority are cleared with a note", func(t *testing.T) {
		root := t.TempDir()
		writeProjectLSP(t, root, "servers:\n  s:\n    command: x\n    env_passthrough: [HOME]\n    priority: 10\n")
		m, notes := LoadProjectServers(root)
		if len(m) != 1 {
			t.Fatalf("m=%v", m)
		}
		if len(m["s"].EnvPassthrough) != 0 || m["s"].Priority != nil {
			t.Fatalf("env_passthrough=%v priority=%v", m["s"].EnvPassthrough, m["s"].Priority)
		}
		if len(notes) != 1 || notes[0] != "s: env_passthrough and priority are ignored in project files" {
			t.Fatalf("notes=%q", notes)
		}
	})
}

func TestProjectServerFingerprint(t *testing.T) {
	// Two spellings of one configuration: struct fields filled in a
	// different order, an env map with a different insertion order, JSON
	// documents with different whitespace and key order.
	a := appcfg.LSPServerConfig{
		Command: "/bin/fake",
		Args:    []string{"--a", "--b"},
	}
	a.Env = map[string]string{"Z": "1", "A": "2"}
	a.InitializationOptions = appcfg.LSPJSONObject(`{"x": 1,  "y": 2}`)
	a.Settings = appcfg.LSPJSONObject(`{"b":2,"a":1}`)

	b := appcfg.LSPServerConfig{Env: map[string]string{"A": "2", "Z": "1"}}
	b.Settings = appcfg.LSPJSONObject(`{ "a": 1, "b": 2 }`)
	b.InitializationOptions = appcfg.LSPJSONObject(`{"y":2,"x":1}`)
	b.Args = []string{"--a", "--b"}
	b.Command = "/bin/fake"

	if ProjectServerFingerprint(a) != ProjectServerFingerprint(b) {
		t.Fatalf("equivalent entries hashed differently: %s != %s", ProjectServerFingerprint(a), ProjectServerFingerprint(b))
	}

	changedArgs := a
	changedArgs.Args = []string{"--a", "--c"}
	if ProjectServerFingerprint(a) == ProjectServerFingerprint(changedArgs) {
		t.Fatal("changing args must change the fingerprint")
	}
	changedSettings := a
	changedSettings.Settings = appcfg.LSPJSONObject(`{"a":1,"b":3}`)
	if ProjectServerFingerprint(a) == ProjectServerFingerprint(changedSettings) {
		t.Fatal("changing settings must change the fingerprint")
	}

	// A field a project entry may not set must not rotate a decision.
	ignored := a
	ten := 10
	ignored.Priority = &ten
	ignored.EnvPassthrough = []string{"HOME"}
	if ProjectServerFingerprint(a) != ProjectServerFingerprint(ignored) {
		t.Fatal("priority and env_passthrough must not change the fingerprint")
	}
}

func TestProjectConsentStore(t *testing.T) {
	ws := t.TempDir()
	c := ProjectConsents{}
	c.Decide("proj", "Gopls", "fp1", ProjectConsentAllow, time.Unix(1000, 0))
	if err := SaveProjectConsents(ws, c); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := LoadProjectConsents(ws)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if decision, decided := loaded.Decision("proj", "gopls", "fp1"); !decided || decision != ProjectConsentAllow {
		t.Fatalf("decision=%q decided=%v", decision, decided)
	}
	// A different fingerprint is undecided again.
	if _, decided := loaded.Decision("proj", "gopls", "fp2"); decided {
		t.Fatal("changed fingerprint must be undecided")
	}
	// The stored shape round-trips.
	if rec := loaded["proj"]["gopls"]; rec.Fingerprint != "fp1" || rec.Decision != ProjectConsentAllow || rec.DecidedAt != 1000 {
		t.Fatalf("record=%+v", rec)
	}
	// Invalid JSON is an empty store, not an error.
	if err := os.WriteFile(ProjectConsentPath(ws), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty, err := LoadProjectConsents(ws)
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty=%v err=%v", empty, err)
	}
}

func TestResolveProjectServers(t *testing.T) {
	t.Run("untrusted yields the zero state without reading the file", func(t *testing.T) {
		root := t.TempDir()
		p := writeProjectLSP(t, root, "servers:\n  s:\n    command: x\n")
		if err := os.Chmod(p, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
		state := ResolveProjectServers(t.TempDir(), root, "proj", false, false)
		if !reflect.DeepEqual(state, ProjectServerState{}) {
			t.Fatalf("state=%+v, want the zero value", state)
		}
	})
	t.Run("allow, deny and pending", func(t *testing.T) {
		root := t.TempDir()
		writeProjectLSP(t, root, `servers:
  one:
    command: /bin/one
  two:
    args: ["-x"]
  three:
    command: /bin/three
`)
		ws := t.TempDir()
		consents := ProjectConsents{}
		consents.Decide("proj", "one", ProjectServerFingerprint(mustLoadProjectEntry(t, root, "one")), ProjectConsentAllow, time.Unix(1, 0))
		consents.Decide("proj", "two", ProjectServerFingerprint(mustLoadProjectEntry(t, root, "two")), ProjectConsentDeny, time.Unix(1, 0))
		if err := SaveProjectConsents(ws, consents); err != nil {
			t.Fatal(err)
		}
		state := ResolveProjectServers(ws, root, "proj", true, false)
		if len(state.Allowed) != 1 || state.Allowed["one"].Command != "/bin/one" {
			t.Fatalf("allowed=%v", state.Allowed)
		}
		if len(state.Denied) != 1 || state.Denied[0] != "two" {
			t.Fatalf("denied=%v", state.Denied)
		}
		if len(state.Pending) != 1 || state.Pending[0].ID != "three" {
			t.Fatalf("pending=%+v", state.Pending)
		}
		if want := "three: runs `/bin/three`"; state.Pending[0].Summary != want {
			t.Fatalf("summary=%q want %q", state.Pending[0].Summary, want)
		}
	})
	t.Run("summaries cover settings overrides and enable requests", func(t *testing.T) {
		root := t.TempDir()
		writeProjectLSP(t, root, `servers:
  gopls:
    settings: {gopls: {buildFlags: ["-tags=integration"]}}
  pyright:
    command: /bin/pyright
    enabled: true
`)
		state := ResolveProjectServers(t.TempDir(), root, "proj", true, false)
		byID := map[string]PendingProjectServer{}
		for _, p := range state.Pending {
			byID[p.ID] = p
		}
		if s := byID["gopls"].Summary; s != "gopls: changes settings of the built-in gopls server" {
			t.Fatalf("gopls summary=%q", s)
		}
		if s := byID["pyright"].Summary; s != "pyright: runs `/bin/pyright` and enables it" {
			t.Fatalf("pyright summary=%q", s)
		}
	})
}

// mustLoadProjectEntry loads the project file and returns one entry, for
// tests that fingerprint exactly what the loader produced.
func mustLoadProjectEntry(t *testing.T, root, id string) appcfg.LSPServerConfig {
	t.Helper()
	m, notes := LoadProjectServers(root)
	if len(notes) != 0 {
		t.Fatalf("notes=%q", notes)
	}
	srv, ok := m[id]
	if !ok {
		t.Fatalf("entry %q missing: %v", id, m)
	}
	return srv
}

// DecideProjectServersFor decides the ids it is given and nothing else: an
// entry it does not name keeps its own state, pending included.
func TestDecideProjectServersForOnlyTouchesNamed(t *testing.T) {
	root := t.TempDir()
	writeProjectLSP(t, root, `servers:
  one:
    command: /bin/one
  two:
    command: /bin/two
`)
	ws := t.TempDir()
	if err := DecideProjectServersFor(ws, root, "proj", []string{"one"}, nil, nil); err != nil {
		t.Fatalf("decide: %v", err)
	}
	state := ResolveProjectServers(ws, root, "proj", true, false)
	if len(state.Denied) != 1 || state.Denied[0] != "one" {
		t.Fatalf("denied=%v", state.Denied)
	}
	if len(state.Pending) != 1 || state.Pending[0].ID != "two" {
		t.Fatalf("pending=%+v, want the unnamed entry still awaiting a decision", state.Pending)
	}
	if len(state.Allowed) != 0 {
		t.Fatalf("allowed=%v", state.Allowed)
	}
}

// A named decision overwrites the one an entry already has, so a denied entry
// can be allowed again without changing the file.
func TestDecideProjectServersForAllowsADeniedEntry(t *testing.T) {
	root := t.TempDir()
	writeProjectLSP(t, root, `servers:
  one:
    command: /bin/one
  two:
    command: /bin/two
`)
	ws := t.TempDir()
	// The startup prompt's answer: nothing allowed, so both are denied.
	if err := DecideProjectServers(ws, root, "proj", nil); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if state := ResolveProjectServers(ws, root, "proj", true, false); len(state.Denied) != 2 {
		t.Fatalf("denied=%v", state.Denied)
	}
	if err := DecideProjectServersFor(ws, root, "proj", []string{"one"}, []string{"one"}, nil); err != nil {
		t.Fatalf("decide: %v", err)
	}
	state := ResolveProjectServers(ws, root, "proj", true, false)
	if len(state.Allowed) != 1 || state.Allowed["one"].Command != "/bin/one" {
		t.Fatalf("allowed=%v", state.Allowed)
	}
	if len(state.Denied) != 1 || state.Denied[0] != "two" {
		t.Fatalf("denied=%v", state.Denied)
	}
}

// A named decision is bound to the entry the surface showed: once the file
// moves, the call records nothing and names what went stale, and the retry
// with the fingerprint the file now has succeeds.
func TestDecideProjectServersForRefusesAChangedEntry(t *testing.T) {
	root := t.TempDir()
	path := writeProjectLSP(t, root, "servers:\n  one: {command: /bin/one}\n  two: {command: /bin/two}\n")
	ws := t.TempDir()
	pendingFP := func(id string) string {
		t.Helper()
		for _, item := range ResolveProjectServers(ws, root, "proj", true, false).Pending {
			if item.ID == id {
				return item.Fingerprint
			}
		}
		t.Fatalf("no pending entry %q", id)
		return ""
	}
	fp1 := pendingFP("one")

	if err := os.WriteFile(path, []byte("servers:\n  one: {command: /bin/changed}\n  two: {command: /bin/two}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := DecideProjectServersFor(ws, root, "proj", []string{"one"}, []string{"one"}, map[string]string{"one": fp1})
	var stale *StaleConsentError
	if !errors.As(err, &stale) {
		t.Fatalf("err = %v, want *StaleConsentError", err)
	}
	if !reflect.DeepEqual(stale.StaleNames(), []string{"one"}) {
		t.Fatalf("stale names = %v", stale.StaleNames())
	}
	if _, err := os.Stat(ProjectConsentPath(ws)); !os.IsNotExist(err) {
		t.Fatalf("a refused decision must not write a consent store: %v", err)
	}
	// two keeps awaiting its own decision as well: a stale name spoils the
	// whole call, not just itself.
	if state := ResolveProjectServers(ws, root, "proj", true, false); len(state.Pending) != 2 {
		t.Fatalf("nothing may be recorded, so both entries stay pending: %+v", state.Pending)
	}

	fp2 := pendingFP("one")
	if fp2 == fp1 {
		t.Fatal("rewriting the command must change the fingerprint")
	}
	if err := DecideProjectServersFor(ws, root, "proj", []string{"one"}, []string{"one"}, map[string]string{"one": fp2}); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if state := ResolveProjectServers(ws, root, "proj", true, false); len(state.Allowed) != 1 || state.Allowed["one"].Command != "/bin/changed" {
		t.Fatalf("allowed=%v", state.Allowed)
	}
}

func TestDecideProjectServers(t *testing.T) {
	root := t.TempDir()
	writeProjectLSP(t, root, `servers:
  keep:
    command: /bin/keep
  drop:
    command: /bin/drop
`)
	ws := t.TempDir()
	if err := DecideProjectServers(ws, root, "proj", []string{"keep"}); err != nil {
		t.Fatalf("decide: %v", err)
	}
	state := ResolveProjectServers(ws, root, "proj", true, false)
	if len(state.Allowed) != 1 || state.Allowed["keep"].Command != "/bin/keep" {
		t.Fatalf("allowed=%v", state.Allowed)
	}
	if len(state.Denied) != 1 || state.Denied[0] != "drop" {
		t.Fatalf("denied=%v", state.Denied)
	}
	if len(state.Pending) != 0 {
		t.Fatalf("pending=%+v", state.Pending)
	}

	// The store is JSON of the documented shape.
	b, err := os.ReadFile(ProjectConsentPath(ws))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]map[string]ProjectConsentRecord
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("store shape: %v", err)
	}
	if rec := doc["proj"]["keep"]; rec.Decision != ProjectConsentAllow || rec.DecidedAt == 0 {
		t.Fatalf("keep record=%+v", rec)
	}
	if rec := doc["proj"]["drop"]; rec.Decision != ProjectConsentDeny {
		t.Fatalf("drop record=%+v", rec)
	}

	// Decisions are keyed by fingerprint, so a later decide changes nothing
	// while the file is unchanged: a denial is not re-asked (and not
	// flipped) until .forebrain/lsp_servers.yaml changes the entry.
	if err := DecideProjectServers(ws, root, "proj", []string{"drop"}); err != nil {
		t.Fatal(err)
	}
	state = ResolveProjectServers(ws, root, "proj", true, false)
	if len(state.Allowed) != 1 || state.Allowed["keep"].Command != "/bin/keep" {
		t.Fatalf("allowed after re-decide=%v", state.Allowed)
	}
	if len(state.Denied) != 1 || state.Denied[0] != "drop" {
		t.Fatalf("denied after re-decide=%v", state.Denied)
	}

	// Changing the entry rotates its fingerprint: drop asks again, and this
	// time it is allowed; keep's decision is untouched.
	writeProjectLSP(t, root, "servers:\n  keep:\n    command: /bin/keep\n  drop:\n    command: /bin/drop2\n")
	if err := DecideProjectServers(ws, root, "proj", []string{"drop"}); err != nil {
		t.Fatal(err)
	}
	state = ResolveProjectServers(ws, root, "proj", true, false)
	if len(state.Allowed) != 2 || state.Allowed["drop"].Command != "/bin/drop2" {
		t.Fatalf("allowed after the file changed=%v", state.Allowed)
	}
	if len(state.Pending) != 0 || len(state.Denied) != 0 {
		t.Fatalf("pending=%+v denied=%v", state.Pending, state.Denied)
	}
}

// The project's "always allow" switch lets an undecided entry through while
// an explicit denial still wins.
func TestResolveProjectServersAutoAllow(t *testing.T) {
	root := t.TempDir()
	writeProjectLSP(t, root, "servers:\n  one:\n    command: /bin/one\n  two:\n    command: /bin/two\n")
	ws := t.TempDir()
	consents := ProjectConsents{}
	consents.Decide("proj", "two", ProjectServerFingerprint(mustLoadProjectEntry(t, root, "two")), ProjectConsentDeny, time.Unix(1, 0))
	if err := SaveProjectConsents(ws, consents); err != nil {
		t.Fatal(err)
	}
	state := ResolveProjectServers(ws, root, "proj", true, true)
	if len(state.Allowed) != 1 || state.Allowed["one"].Command != "/bin/one" {
		t.Fatalf("undecided entry must land in Allowed under the switch: %+v", state.Allowed)
	}
	if len(state.Pending) != 0 {
		t.Fatalf("nothing stays pending under the switch: %+v", state.Pending)
	}
	if len(state.Denied) != 1 || state.Denied[0] != "two" {
		t.Fatalf("a denied entry must stay denied: %v", state.Denied)
	}
}
