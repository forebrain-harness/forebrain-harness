// Text an engine refuses is repaired at the store boundary.
//
// A state database now runs on MySQL 8, PostgreSQL 14 or SQLite, and the first
// two reject text SQLite stores happily: invalid UTF-8 bytes (MySQL error 1366,
// PostgreSQL SQLSTATE 22021) and a NUL inside a value. Tools produce exactly
// that — read_file refuses a NUL only in a file's first 8 KiB and never repairs
// non-UTF-8 bytes, and shell output is not repaired at all — so these tests pin
// what the store writes, not what a tool hands it.
package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
)

// TestStoreRepairsTextAnEngineWouldReject pins the promise the store boundary
// makes: the text it writes is text every engine accepts.
//
// On SQLite both bad bytes round-trip, which is why this has never been seen on
// the default engine; MySQL and PostgreSQL reject the row server-side instead.
// The assertion is therefore "the stored value is what an engine accepts", not
// "the value is unchanged" — a normalisation that only ran on the two servers
// would make a conversation's stored bytes depend on which one it ran on.
func TestStoreRepairsTextAnEngineWouldReject(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open the state database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	t.Logf("engine under test: %s", dbx.DialectOf(db))
	ensureSessionsForRuns(t, db, "bytes-session")
	store := NewSessionStore(db, "main")

	const bad = "before\xffafter\x00end"
	message := llm.UserMessage(llm.Text(bad))
	if err := store.AppendMessageSequenceForRun(ctx, "bytes-session", "", []llm.Message{message}, "model", ""); err != nil {
		t.Fatalf("a message carrying an invalid byte and a NUL was refused instead of repaired: %v", err)
	}

	content, parts := storedRowText(t, db, "bytes-session")
	if !utf8.ValidString(content) {
		t.Errorf("stored content is not valid UTF-8: %q", content)
	}
	if strings.ContainsRune(content, 0) {
		t.Errorf("stored content carries a NUL: %q", content)
	}
	if !utf8.ValidString(parts) || strings.ContainsRune(parts, 0) {
		t.Errorf("stored parts carry a byte an engine refuses: %q", parts)
	}
	if strings.Contains(parts, `\u0000`) {
		t.Errorf("stored parts carry a NUL escape, which PostgreSQL refuses to parse out of a jsonb cast: %q", parts)
	}
	if !strings.HasPrefix(content, "before") || !strings.HasSuffix(content, "end") {
		t.Errorf("stored content lost the prose around the bad bytes: %q", content)
	}
}

// storedRowText reads the content and parts columns of a conversation's first
// message row back as the engine stored them.
func storedRowText(t *testing.T, db *sql.DB, sessionID string) (content, parts string) {
	t.Helper()
	query := `SELECT content, parts FROM fb_messages WHERE session_id=` +
		dbx.Placeholder(dbx.DialectOf(db), 1) + ` ORDER BY id ASC`
	if err := db.QueryRowContext(context.Background(), query, sessionID).Scan(&content, &parts); err != nil {
		t.Fatalf("read the stored row back: %v", err)
	}
	return content, parts
}

// TestValidTextRepairsWhatAnEngineRefusesAndNothingElse pins both halves of the
// helper: the bytes MySQL and PostgreSQL reject are replaced, and everything
// else — prose, whitespace, a 4-byte character — comes back byte-identical. The
// second half is the one that matters most: a repair that rewrote valid text
// would change every conversation's stored history.
func TestValidTextRepairsWhatAnEngineRefusesAndNothingElse(t *testing.T) {
	unchanged := []string{
		"",
		"plain prose, with punctuation — and an em dash",
		"line one\nline two\r\nindented\tcell",
		"emoji \U0001F680 and a combining mark é",
		"already repaired \uFFFD",
	}
	for _, in := range unchanged {
		if got := ValidText(in); got != in {
			t.Errorf("ValidText(%q) = %q, want it unchanged", in, got)
		}
	}

	repaired := []struct {
		name string
		in   string
		want string
	}{
		{"invalid byte", "before\xffafter", "before" + ReplacementText + "after"},
		// ToValidUTF8 replaces a whole run of invalid bytes with one
		// replacement character, which is what a reader of truncated text wants.
		{"truncated sequence", "before\xe4\xb8after", "before" + ReplacementText + "after"},
		{"NUL", "before\x00after", "before" + ReplacementText + "after"},
		{"NUL beside an invalid byte", "\x00\xff", ReplacementText + ReplacementText},
	}
	for _, tc := range repaired {
		got := ValidText(tc.in)
		if got != tc.want {
			t.Errorf("%s: ValidText(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
		if !utf8.ValidString(got) || strings.ContainsRune(got, 0) {
			t.Errorf("%s: ValidText(%q) = %q, which an engine still refuses", tc.name, tc.in, got)
		}
	}
}

// TestValidJSONTextReplacesTheNULEscapeAndKeepsOtherEscapes pins the JSON form:
// the escape a NUL takes inside a stored document is replaced, an escaped
// backslash followed by the text `u0000` is a different document and survives,
// and every other escape is left to the JSON encoder and decoder.
func TestValidJSONTextReplacesTheNULEscapeAndKeepsOtherEscapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "NUL escape replaced",
			in:   `{"type":"text","text":"before\u0000after"}`,
			want: `{"type":"text","text":"before\uFFFDafter"}`,
		},
		{
			name: "doubled backslash is not the escape",
			in:   `{"text":"literal \\u0000 stays"}`,
			want: `{"text":"literal \\u0000 stays"}`,
		},
		{
			name: "ordinary escapes survive",
			in:   `{"text":"a\nb\tc\r\"d\"é\ud83d\ude80"}`,
			want: `{"text":"a\nb\tc\r\"d\"é\ud83d\ude80"}`,
		},
		{
			name: "an escaped backslash before a real NUL escape",
			in:   `{"text":"a\\\u0000b"}`,
			want: `{"text":"a\\\uFFFDb"}`,
		},
		{
			name: "invalid byte and raw NUL in the document",
			in:   "{\"text\":\"a\xffb\x00c\"}",
			want: `{"text":"a` + ReplacementText + `b` + ReplacementText + `c"}`,
		},
	}
	for _, tc := range cases {
		got := string(ValidJSONText([]byte(tc.in)))
		if got != tc.want {
			t.Errorf("%s: ValidJSONText(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: ValidJSONText(%q) is not valid UTF-8: %q", tc.name, tc.in, got)
		}
		if strings.Contains(got, `\u0000`) && tc.want != tc.in {
			t.Errorf("%s: ValidJSONText(%q) still carries the escape: %q", tc.name, tc.in, got)
		}
	}
	// A repaired document is still a document: the value decodes back with the
	// replacement character where the NUL was.
	var decoded struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(ValidJSONText([]byte(`{"text":"a\u0000b"}`)), &decoded); err != nil {
		t.Fatalf("the repaired document does not parse: %v", err)
	}
	if decoded.Text != "a"+ReplacementText+"b" {
		t.Errorf("repaired document decodes as %q, want a%sb", decoded.Text, ReplacementText)
	}
}

// TestPostgresToolPayloadWithANULStillListsTheToolCall is the engine leg for the
// JSON repair, named for the engine whose jsonb cast is what refused the row: a
// tool completion whose output held a NUL is stored, and the audit reader that
// parses the payload to find the tool's name still returns it.
func TestPostgresToolPayloadWithANULStillListsTheToolCall(t *testing.T) {
	ctx := context.Background()
	db, err := OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open the state database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	t.Logf("engine under test: %s", dbx.DialectOf(db))
	ensureSessionsForRuns(t, db, "tool-payload-session")
	store := &RunStore{DB: db}

	payload := event.EncodePayload(event.ToolCallCompletedPayload{
		ToolName:    "read_file",
		Description: "read a GBK file",
		Data:        "binary\x00output\xffand prose",
	})
	if !strings.Contains(string(payload), `\u0000`) {
		t.Fatalf("the payload under test carries no NUL escape: %s", payload)
	}
	if _, err := store.AppendSessionEvent(ctx, SessionEvent{
		ID:        "evt-nul-payload",
		SessionID: "tool-payload-session",
		Type:      event.RunEventToolCompleted,
		Payload:   payload,
	}); err != nil {
		t.Fatalf("a tool completion whose output held a NUL was refused instead of repaired: %v", err)
	}

	rows, err := store.ListSessionToolCalls(ctx, "tool-payload-session", "", "", 10)
	if err != nil {
		t.Fatalf("list the conversation's tool calls: %v", err)
	}
	if len(rows) != 1 || rows[0].ToolName != "read_file" {
		t.Fatalf("tool calls = %+v, want the one read_file completion", rows)
	}
	if strings.Contains(rows[0].DetailJSON, `\u0000`) {
		t.Errorf("stored payload still carries the NUL escape: %s", rows[0].DetailJSON)
	}
	if !strings.Contains(rows[0].DetailJSON, ReplacementText) {
		t.Errorf("stored payload does not carry the replacement where the NUL was: %s", rows[0].DetailJSON)
	}
}
