package tui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/stretchr/testify/require"
)

// The keys answer as the panel promises: Enter takes the action the cursor is
// on — the panel opens on Quit, so a bare Enter quits — Down moves to Trust
// and continue, and Esc and Ctrl+C both answer Quit.
func TestWorkspaceTrustReviewKeys(t *testing.T) {
	trusted, err := pickerKeys(t, keyEnter).ReviewWorkspaceTrust("/tmp/project")
	require.NoError(t, err)
	require.False(t, trusted, "Enter on the opening cursor must quit, not trust")

	trusted, err = pickerKeys(t, keyDown+keyEnter).ReviewWorkspaceTrust("/tmp/project")
	require.NoError(t, err)
	require.True(t, trusted, "Down then Enter must trust")

	trusted, err = pickerKeys(t, "\x1b").ReviewWorkspaceTrust("/tmp/project")
	require.NoError(t, err)
	require.False(t, trusted, "Esc must quit")

	trusted, err = pickerKeys(t, "\x03").ReviewWorkspaceTrust("/tmp/project")
	require.NoError(t, err)
	require.False(t, trusted, "Ctrl+C must quit")
}

// The panel is the owner's chosen picture: the question, one sentence of what
// trusting allows, the folder — the home directory abbreviated to ~ — and the
// two actions with the cursor on Quit, under a hint that says Esc quits.
func TestWorkspaceTrustPanelPaintsTheChosenPicture(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer r.Close()
	defer w.Close()
	go func() {
		_, _ = w.WriteString(keyEnter)
	}()

	var out bytes.Buffer
	sel := newStandaloneRawSelector(r, &out)
	trusted, err := sel.ReviewWorkspaceTrust(filepath.Join(homeDirForDisplay(t), "trust-panel-project"))
	require.NoError(t, err)
	require.False(t, trusted)

	// The subtitle wraps to the width; flatten the page — every CSI sequence
	// gone — so the assertions speak about its words, not its line breaks.
	flat := strings.Join(strings.Fields(stripAllANSI(out.String())), " ")
	for _, want := range []string{
		"Trust this folder?",
		"Forebrain Harness can edit files here and follow the folder's own instructions and skills.",
		"Path:",
		"~/trust-panel-project",
		"❯ Quit",
		"Trust and continue",
	} {
		require.Contains(t, flat, want)
	}
	require.False(t, strings.Contains(flat, "❯ Trust and continue"), "the cursor must open on Quit, not on Trust and continue")
	hint := "↑/↓ to navigate · Enter to confirm · Esc to quit"
	at := strings.LastIndex(flat, hint)
	require.GreaterOrEqual(t, at, 0, "the hint line must be drawn")
	require.False(t, strings.Contains(flat[at+len(hint):], "Esc to cancel"), "the hint must end with Esc to quit")
}

// homeDirForDisplay is the home the ~ abbreviation reads, so the test speaks
// about the same directory the operator would see.
func homeDirForDisplay(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	return home
}

// trustRecordingSession is the session half: whether a decision is pending,
// what recording a trust saw.
type trustRecordingSession struct {
	*fakeSession
	folder     string
	pending    bool
	pendingErr error
	trustErr   error
	trustCalls int
}

func (s *trustRecordingSession) WorkspaceTrustPending() (string, bool, error) {
	return s.folder, s.pending, s.pendingErr
}

func (s *trustRecordingSession) TrustWorkspace() error {
	s.trustCalls++
	return s.trustErr
}

// trustRecordingSelector is the selector half: it records the folder it was
// asked about and answers as configured.
type trustRecordingSelector struct {
	recordingSelector
	folder   string
	reviews  int
	trusted  bool
	reviewEr error
}

func (s *trustRecordingSelector) ReviewWorkspaceTrust(folder string) (bool, error) {
	s.reviews++
	s.folder = folder
	return s.trusted, s.reviewEr
}

func runAskWorkspaceTrust(t *testing.T, session *trustRecordingSession, selector *trustRecordingSelector) (bool, error) {
	t.Helper()
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	cmds := newCommandController(session, renderer, selector, nil, t.TempDir())
	return askWorkspaceTrust(cmds, renderer)
}

// The orchestration asks only while no decision exists, records exactly one
// trust, and fails closed: a folder whose trust store cannot be read is not
// entered.
func TestAskWorkspaceTrust(t *testing.T) {
	// Undecided and Quit: the surface quits and nothing is recorded.
	session := &trustRecordingSession{fakeSession: &fakeSession{}, folder: "/tmp/project", pending: true}
	selector := &trustRecordingSelector{}
	quit, err := runAskWorkspaceTrust(t, session, selector)
	require.NoError(t, err)
	require.True(t, quit)
	require.Equal(t, 1, selector.reviews)
	require.Equal(t, "/tmp/project", selector.folder)
	require.Zero(t, session.trustCalls)

	// Undecided and Trust: recorded once, the surface carries on.
	session = &trustRecordingSession{fakeSession: &fakeSession{}, folder: "/tmp/project", pending: true}
	selector = &trustRecordingSelector{trusted: true}
	quit, err = runAskWorkspaceTrust(t, session, selector)
	require.NoError(t, err)
	require.False(t, quit)
	require.Equal(t, 1, session.trustCalls)

	// A session that offers no trust half, or a selector that cannot ask,
	// changes nothing.
	plain := &fakeProjectServersSession{fakeSession: &fakeSession{}}
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)
	cmds := newCommandController(plain, renderer, &trustRecordingSelector{}, nil, t.TempDir())
	quit, err = askWorkspaceTrust(cmds, renderer)
	require.NoError(t, err)
	require.False(t, quit)

	// Already decided — trusted on a previous launch, or rejected on the
	// project page — is not asked about again.
	decided := &trustRecordingSession{fakeSession: &fakeSession{}, folder: "/tmp/project"}
	selector = &trustRecordingSelector{}
	quit, err = runAskWorkspaceTrust(t, decided, selector)
	require.NoError(t, err)
	require.False(t, quit)
	require.Zero(t, selector.reviews, "a decided folder must not be asked about")

	// Reading the trust store failed: fail closed.
	failing := &trustRecordingSession{fakeSession: &fakeSession{}, folder: "/tmp/project", pending: true, pendingErr: errors.New("trust store unreadable")}
	selector = &trustRecordingSelector{trusted: true}
	quit, err = runAskWorkspaceTrust(t, failing, selector)
	require.Error(t, err)
	require.True(t, quit, "a folder whose decision cannot be read must not be entered")
	require.Zero(t, selector.reviews)
	require.Zero(t, failing.trustCalls)

	// The panel itself failing quits the surface with the error.
	session = &trustRecordingSession{fakeSession: &fakeSession{}, folder: "/tmp/project", pending: true}
	selector = &trustRecordingSelector{reviewEr: errors.New("terminal gone")}
	quit, err = runAskWorkspaceTrust(t, session, selector)
	require.Error(t, err)
	require.True(t, quit)
	require.Zero(t, session.trustCalls)

	// Recording the trust failing quits the surface with the error.
	session = &trustRecordingSession{fakeSession: &fakeSession{}, folder: "/tmp/project", pending: true, trustErr: errors.New("state unwritable")}
	selector = &trustRecordingSelector{trusted: true}
	quit, err = runAskWorkspaceTrust(t, session, selector)
	require.Error(t, err)
	require.True(t, quit)
	require.Equal(t, 1, session.trustCalls)
}

// TestWorkspaceTrustFolderIsTheLaunchDirectory pins the launch contract (R2)
// at the trust question: the folder asked about is the directory the TUI
// started in — a subdirectory of a larger checkout, never the checkout a
// plain .git walk would climb to — and a recorded decision, either way, ends
// the question.
func TestWorkspaceTrustFolderIsTheLaunchDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FOREBRAIN_HOME", home)
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	sub := filepath.Join(repo, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	s, err := OpenChatSessionWithConfigForProject(context.Background(), testConfig(), sub)
	require.NoError(t, err)
	defer s.Close()

	want := sub
	if resolved, err := filepath.EvalSymlinks(sub); err == nil && strings.TrimSpace(resolved) != "" {
		want = filepath.Clean(resolved)
	}

	folder, pending, err := s.WorkspaceTrustPending()
	require.NoError(t, err)
	require.True(t, pending, "a folder nobody has judged must be pending")
	require.Equal(t, want, folder)

	// Trusting it records exactly that folder and ends the question.
	require.NoError(t, s.TrustWorkspace())
	folder, pending, err = s.WorkspaceTrustPending()
	require.NoError(t, err)
	require.False(t, pending)
	require.Equal(t, want, folder)
	level, err := safety.TrustLevel(home, safety.Project{Root: want})
	require.NoError(t, err)
	require.Equal(t, safety.LevelTrusted, level)
	require.Equal(t, safety.LevelTrusted, s.LaunchProject.TrustLevel)

	// A recorded rejection ends the question the same way.
	repo2 := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo2, ".git"), 0o755))
	sub2 := filepath.Join(repo2, "sub")
	require.NoError(t, os.MkdirAll(sub2, 0o755))
	require.NoError(t, safety.MarkUntrusted(home, safety.Project{Root: sub2}))
	s2, err := OpenChatSessionWithConfigForProject(context.Background(), testConfig(), sub2)
	require.NoError(t, err)
	defer s2.Close()
	_, pending, err = s2.WorkspaceTrustPending()
	require.NoError(t, err)
	require.False(t, pending, "a rejected folder is not asked about; its approval policy holds it back")
}

// startupQuestionsSession is the session half of every startup question,
// recording the order they run in.
type startupQuestionsSession struct {
	*fakeSession
	order []string
}

func (s *startupQuestionsSession) record(step string) {
	s.order = append(s.order, step)
}

func (s *startupQuestionsSession) WorkspaceTrustPending() (string, bool, error) {
	s.record("trust:pending")
	return "/tmp/project", true, nil
}

func (s *startupQuestionsSession) TrustWorkspace() error {
	s.record("trust:recorded")
	return nil
}

func (s *startupQuestionsSession) AdoptLaunchProject(context.Context) error {
	s.record("adopt")
	return nil
}

func (s *startupQuestionsSession) PendingProjectServers() []projectServerItem {
	s.record("servers:pending")
	return []projectServerItem{{Kind: projectServerMCP, Name: "projtool", Detail: "/bin/pt", Fingerprint: "fp"}}
}

func (s *startupQuestionsSession) DecideProjectServers([]projectServerItem, projectServersAnswer) error {
	s.record("servers:decided")
	return nil
}

func (s *startupQuestionsSession) BindConversationRuntime(_ context.Context, sessionID string) error {
	s.record("bind:" + sessionID)
	return nil
}

// startupQuestionsSelector is the selector half, recording its panels on the
// same order the session records its steps.
type startupQuestionsSelector struct {
	recordingSelector
	session *startupQuestionsSession
	trusted bool
}

func (s *startupQuestionsSelector) ReviewWorkspaceTrust(string) (bool, error) {
	s.session.record("trust:panel")
	return s.trusted, nil
}

func (s *startupQuestionsSelector) ReviewProjectServers([]projectServerItem) (projectServersAnswer, error) {
	s.session.record("servers:panel")
	return projectServersAnswer{Decided: true, Allowed: []bool{true}}, nil
}

// The startup questions run in the order the first conversation depends on —
// trust, project registration, project servers, the conversation's runtime —
// and a Quit at the trust question leaves none of the rest run.
func TestStartupQuestionsRunTrustAdoptServersBind(t *testing.T) {
	ctx := context.Background()
	var out bytes.Buffer
	renderer := NewRenderer(&out, &out)

	// Trusted: every step runs, in order, and the runtime binds the session.
	session := &startupQuestionsSession{fakeSession: &fakeSession{}}
	selector := &startupQuestionsSelector{session: session, trusted: true}
	cmds := newCommandController(session, renderer, selector, nil, t.TempDir())
	require.NoError(t, startupQuestions(ctx, Options{Session: session}, cmds, renderer, "sid-1"))
	require.Equal(t, []string{
		"trust:pending", "trust:panel", "trust:recorded",
		"adopt",
		"servers:pending", "servers:panel", "servers:decided",
		"bind:sid-1",
	}, session.order)

	// Quit at the trust question: nothing that follows ever runs.
	session = &startupQuestionsSession{fakeSession: &fakeSession{}}
	selector = &startupQuestionsSelector{session: session, trusted: false}
	cmds = newCommandController(session, renderer, selector, nil, t.TempDir())
	err := startupQuestions(ctx, Options{Session: session}, cmds, renderer, "sid-1")
	require.Equal(t, ErrCancelled, err)
	require.Equal(t, []string{"trust:pending", "trust:panel"}, session.order)

	// A resumed session adopts its project before the TUI opens: no adopt
	// here, everything else unchanged.
	session = &startupQuestionsSession{fakeSession: &fakeSession{}}
	selector = &startupQuestionsSelector{session: session, trusted: true}
	cmds = newCommandController(session, renderer, selector, nil, t.TempDir())
	require.NoError(t, startupQuestions(ctx, Options{Session: session, InitialSessionID: "resume-me"}, cmds, renderer, "sid-1"))
	require.Equal(t, []string{
		"trust:pending", "trust:panel", "trust:recorded",
		"servers:pending", "servers:panel", "servers:decided",
		"bind:sid-1",
	}, session.order)
}
