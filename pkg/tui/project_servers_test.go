package tui

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func sampleProjectServerItems() []projectServerItem {
	return []projectServerItem{
		{Kind: projectServerMCP, Name: "caveman-shrink", Detail: "npx -y caveman-shrink codegraph serve --mcp", Fingerprint: "fp-1"},
		{Kind: projectServerMCP, Name: "github", Detail: "https://api.example.com/mcp", Fingerprint: "fp-2"},
		{Kind: projectServerLSP, Name: "gopls", Detail: "gopls serve", Fingerprint: "fp-3"},
	}
}

// projectServerPanelPlain lays the panel out at width and strips it of style,
// the way the panel tests read a panel's rows.
func projectServerPanelPlain(p slashPanel, width int) []string {
	layout := p.layout(width, 0, 0, false)
	out := make([]string, 0, len(layout.lines))
	for _, line := range layout.lines {
		out = append(out, stripANSI(line))
	}
	return out
}

// requireInOrder asserts that each string appears, in the order given, somewhere
// after the one before it.
func requireInOrder(t *testing.T, lines []string, want ...string) {
	t.Helper()
	at := 0
	for _, w := range want {
		found := -1
		for i := at; i < len(lines); i++ {
			if strings.Contains(lines[i], w) {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("panel missing %q after row %d:\n%s", w, at, strings.Join(lines, "\n"))
		}
		at = found + 1
	}
}

// The panel is the owner's chosen picture: the two columns, the entries
// grouped MCP first then language servers, the switch row apart from the list,
// and the keys as one line.
func TestProjectServersPanelGolden(t *testing.T) {
	st := newProjectServersPanelState(sampleProjectServerItems())
	requireInOrder(t, projectServerPanelPlain(st.panel(), 80),
		"Project servers",
		"3 servers want to run in this project",
		"MCP",
		"❯ ● caveman-shrink",
		"● github",
		"Language servers",
		"● gopls",
		"○ Always allow this project's servers",
		"↑/↓ to navigate · Space to select · Enter to confirm · Esc to decline all",
	)

	// One entry reads as one server and a kind with no entries draws no group.
	one := newProjectServersPanelState(sampleProjectServerItems()[2:])
	plain := projectServerPanelPlain(one.panel(), 80)
	requireInOrder(t, plain, "Project servers", "1 server wants to run in this project", "Language servers", "❯ ● gopls")
	for _, line := range plain {
		if strings.Contains(line, "MCP") {
			t.Fatalf("an empty MCP group must not be drawn:\n%s", strings.Join(plain, "\n"))
		}
	}
}

// Whatever the width, every row fits it whole: the command wraps inside its
// own column, nothing is cut or ellipsized.
func TestProjectServersPanelRowsFitAtEveryWidth(t *testing.T) {
	st := newProjectServersPanelState([]projectServerItem{
		{Kind: projectServerMCP, Name: "caveman-shrink", Detail: "npx -y caveman-shrink codegraph serve --mcp --config /very/long/path/to/a/config/file.json", Fingerprint: "fp"},
		{Kind: projectServerLSP, Name: "typescript-language-server", Detail: "typescript-language-server --stdio --log-level verbose", Fingerprint: "fp"},
	})
	for _, width := range []int{120, 80, 40} {
		plain := projectServerPanelPlain(st.panel(), width)
		assertRowsFit(t, plain, width)
		for _, line := range plain {
			if strings.Contains(line, "…") || strings.Contains(line, "...") {
				t.Fatalf("width %d cut a row: %q", width, line)
			}
		}
	}
}

// The toggle's two linkages hold: turning the switch on checks every entry,
// and an entry left unchecked turns the switch off.
func TestProjectServersPanelStateToggle(t *testing.T) {
	st := newProjectServersPanelState(sampleProjectServerItems())

	// Unchecking an entry drops the switch with it.
	st.move(0)
	st.toggle()
	require.False(t, st.checked[0])
	require.False(t, st.always)

	// Turning the switch on checks every entry.
	st.move(3)
	require.Equal(t, 3, st.cursor, "the cursor must reach the switch row")
	st.toggle()
	require.True(t, st.always)
	require.Equal(t, []bool{true, true, true}, st.checked)

	// answer copies the state as it stands.
	st.always = false
	st.checked[1] = false
	got := st.answer()
	require.True(t, got.Decided)
	require.Equal(t, []bool{true, false, true}, got.Allowed)
	require.False(t, got.AlwaysAllow)
}

// The cursor is clamped to the first entry and the switch row, never wrapping.
func TestProjectServersPanelStateMoveClamps(t *testing.T) {
	st := newProjectServersPanelState(sampleProjectServerItems())
	st.move(-5)
	require.Equal(t, 0, st.cursor)
	st.move(99)
	require.Equal(t, 3, st.cursor, "the last row is the switch")
	st.move(1)
	require.Equal(t, 3, st.cursor)
}

// The keys answer as the panel promises: Enter takes the checks as shown,
// Space flips the row under the cursor with its linkages, Esc declines every
// listed entry and Ctrl+C closes the panel without an answer.
func TestProjectServersReviewKeys(t *testing.T) {
	items := sampleProjectServerItems()

	// Enter alone allows everything, switch off.
	answer, err := pickerKeys(t, keyEnter).ReviewProjectServers(items)
	require.NoError(t, err)
	require.True(t, answer.Decided)
	require.Equal(t, []bool{true, true, true}, answer.Allowed)
	require.False(t, answer.AlwaysAllow)

	// Space unchecked the first entry.
	answer, err = pickerKeys(t, " "+keyEnter).ReviewProjectServers(items)
	require.NoError(t, err)
	require.Equal(t, []bool{false, true, true}, answer.Allowed)
	require.False(t, answer.AlwaysAllow)

	// Down to the switch row, Space turns it on: everything allowed, always.
	answer, err = pickerKeys(t, keyDown+keyDown+keyDown+" "+keyEnter).ReviewProjectServers(items)
	require.NoError(t, err)
	require.Equal(t, []bool{true, true, true}, answer.Allowed)
	require.True(t, answer.AlwaysAllow)

	// With the switch on, unchecking an entry turns it off with the entry.
	answer, err = pickerKeys(t, keyDown+keyDown+keyDown+" "+keyUp+" "+keyEnter).ReviewProjectServers(items)
	require.NoError(t, err)
	require.Equal(t, []bool{true, true, false}, answer.Allowed)
	require.False(t, answer.AlwaysAllow)

	// Esc declines every listed entry.
	answer, err = pickerKeys(t, "\x1b").ReviewProjectServers(items)
	require.NoError(t, err)
	require.True(t, answer.Decided)
	require.Equal(t, []bool{false, false, false}, answer.Allowed)

	// Ctrl+C closes the panel without an answer.
	answer, err = pickerKeys(t, "\x03").ReviewProjectServers(items)
	require.NoError(t, err)
	require.False(t, answer.Decided)
	require.Empty(t, answer.Allowed)
}

// staleProjectServersError is the record's refusal when a project file changed
// under the open panel.
type staleProjectServersError struct{ names []string }

func (e staleProjectServersError) Error() string        { return "stale: " + strings.Join(e.names, ", ") }
func (e staleProjectServersError) StaleNames() []string { return e.names }

// fakeProjectServersSession is the session half: the entries still pending,
// one answer per record call, and what the calls saw.
type fakeProjectServersSession struct {
	*fakeSession
	pending   []projectServerItem
	recordErr []error
	records   []projectServersAnswer
	shown     [][]projectServerItem
}

func (s *fakeProjectServersSession) PendingProjectServers() []projectServerItem {
	return s.pending
}

func (s *fakeProjectServersSession) DecideProjectServers(items []projectServerItem, answer projectServersAnswer) error {
	s.records = append(s.records, answer)
	s.shown = append(s.shown, append([]projectServerItem(nil), items...))
	if len(s.recordErr) == 0 {
		return nil
	}
	err := s.recordErr[0]
	s.recordErr = s.recordErr[1:]
	return err
}

// projectServersRecordingSelector is the selector half: it records every panel
// and answers the one answer it was given.
type projectServersRecordingSelector struct {
	recordingSelector
	reviews int
	listed  [][]projectServerItem
	answer  projectServersAnswer
	err     error
}

func (s *projectServersRecordingSelector) ReviewProjectServers(items []projectServerItem) (projectServersAnswer, error) {
	s.reviews++
	s.listed = append(s.listed, append([]projectServerItem(nil), items...))
	return s.answer, s.err
}

func runAskProjectServers(t *testing.T, session *fakeProjectServersSession, selector *projectServersRecordingSelector) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	cmds := newCommandController(session, renderer, selector, nil, t.TempDir())
	askProjectServers(cmds, renderer)
	return &out
}

// The orchestration asks once, records once, and asks again only when the
// record came back stale.
func TestAskProjectServers(t *testing.T) {
	items := sampleProjectServerItems()
	decided := projectServersAnswer{Decided: true, Allowed: []bool{true, false, true}}

	// Nothing pending: no panel, no record.
	empty := &fakeProjectServersSession{fakeSession: &fakeSession{}}
	selector := &projectServersRecordingSelector{answer: decided}
	runAskProjectServers(t, empty, selector)
	require.Equal(t, 0, selector.reviews, "nothing pending must not open the panel")

	// Closed without an answer: nothing is recorded.
	session := &fakeProjectServersSession{fakeSession: &fakeSession{}, pending: items}
	selector = &projectServersRecordingSelector{}
	runAskProjectServers(t, session, selector)
	require.Equal(t, 1, selector.reviews)
	require.Empty(t, session.records)

	// A stale record asks again with what is still pending; the second one
	// succeeds.
	session = &fakeProjectServersSession{
		fakeSession: &fakeSession{},
		pending:     items,
		recordErr:   []error{staleProjectServersError{names: []string{"github"}}},
	}
	selector = &projectServersRecordingSelector{answer: decided}
	runAskProjectServers(t, session, selector)
	require.Equal(t, 2, selector.reviews, "a stale record must ask again")
	require.Len(t, session.records, 2, "the second record must be taken")
	require.Equal(t, decided, session.records[1])

	// Any other error is shown once, not retried.
	session = &fakeProjectServersSession{
		fakeSession: &fakeSession{},
		pending:     items,
		recordErr:   []error{errors.New("consent store unwritable")},
	}
	selector = &projectServersRecordingSelector{answer: decided}
	runAskProjectServers(t, session, selector)
	require.Equal(t, 1, selector.reviews, "an ordinary error must not be retried")
	require.Len(t, session.records, 1)
}
