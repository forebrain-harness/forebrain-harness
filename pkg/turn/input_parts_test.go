package turn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/agent"
	"github.com/forebrain-harness/forebrain-harness/pkg/assembly"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/safety"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/stretchr/testify/require"
)

type compactStub struct {
	id  string
	err error
	res assembly.Result
}

func (s *compactStub) ManualCompactSession(_ context.Context, id, trigger string) (assembly.Result, error) {
	s.id = id
	if trigger != "manual" {
		return assembly.Result{}, errors.New("unexpected trigger")
	}
	return s.res, s.err
}

func TestExecuteCompact(t *testing.T) {
	stub := &compactStub{res: assembly.Result{Strategy: "local", Summary: "summary", TokensBefore: 182_400, TokensAfter: 12_300, Duration: 14_200 * time.Millisecond}}
	got := ExecuteCompact(context.Background(), "", stub)
	if stub.id != "default" || got.Err != nil || got.Result.Summary != "summary" {
		t.Fatalf("result = %#v, id = %q", got, stub.id)
	}
	want := "Context compacted · 182.4k → 12.3k tokens (−93%) · 14.2s\n\n" + assembly.WarningMessage
	if got.Reply != want {
		t.Fatalf("reply = %q", got.Reply)
	}
}

func TestExecuteCompactFailure(t *testing.T) {
	stub := &compactStub{err: errors.New("boom")}
	got := ExecuteCompact(context.Background(), "s1", stub)
	if !errors.Is(got.Err, stub.err) || got.Reply != "Compaction failed: boom" {
		t.Fatalf("result = %#v", got)
	}
	stub = &compactStub{err: context.Canceled}
	if got := ExecuteCompact(context.Background(), "s1", stub); got.Reply != "Compaction cancelled." {
		t.Fatalf("cancelled reply = %q", got.Reply)
	}
}

func TestSurfaceTurnContentPartsIncludesImageAttachment(t *testing.T) {
	img := filepath.Join(t.TempDir(), "clip.png")
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	if err := os.WriteFile(img, data, 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}

	parts, display, err := SurfaceTurnContentParts("explain", "explain", []InputAttachment{{Path: img, MIMEType: "image/png", Label: "clip.png"}})
	if err != nil {
		t.Fatalf("SurfaceTurnContentParts: %v", err)
	}
	if len(parts) != 2 || parts[0].Type != llm.ContentTypeText || parts[1].Type != llm.ContentTypeImageBase64 {
		t.Fatalf("unexpected parts: %#v", parts)
	}
	if !strings.Contains(display, "[Image #1]") {
		t.Fatalf("display text missing attachment marker: %q", display)
	}
}

func TestBuildSurfaceUserPartsJSONPersistsCanonicalAttachmentParts(t *testing.T) {
	img := filepath.Join(t.TempDir(), "clip.png")
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	if err := os.WriteFile(img, data, 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}

	partsJSON := BuildSurfaceUserPartsJSON(
		"explain this image",
		[]InputAttachment{{Path: img, MIMEType: "image/png", Label: "clip.png"}},
	)

	var parts []map[string]any
	if err := json.Unmarshal([]byte(partsJSON), &parts); err != nil {
		t.Fatalf("unmarshal parts_json: %v", err)
	}
	// Image attachments are delivered to the model as base64 parts
	// (rehydrated from the file_reference), so no redundant
	// "[attachment parsed]" text marker is persisted. Only the text and
	// file_reference parts remain.
	if len(parts) != 2 {
		t.Fatalf("parts = %+v, want 2 (text, file_reference)", parts)
	}
	if parts[0]["type"] != "text" {
		t.Fatalf("parts[0][type] = %v, want text", parts[0]["type"])
	}
	if parts[1]["type"] != state.PartTypeFileReference {
		t.Fatalf("parts[1][type] = %v, want %v", parts[1]["type"], state.PartTypeFileReference)
	}
	if parts[1]["label"] != "clip.png" {
		t.Fatalf("parts[1][label] = %v, want clip.png", parts[1]["label"])
	}
	if parts[1]["mime_type"] != "image/png" {
		t.Fatalf("parts[1][mime_type] = %v, want image/png", parts[1]["mime_type"])
	}
}

func TestDisplayTextOrUserTextPrefersDisplayUnlessItIsASlashRenderingHint(t *testing.T) {
	if got := DisplayTextOrUserText("hello", ""); got != "hello" {
		t.Fatalf("DisplayTextOrUserText(hello, \"\") = %q, want hello", got)
	}
	if got := DisplayTextOrUserText("hello", "shown text"); got != "shown text" {
		t.Fatalf("DisplayTextOrUserText with a plain display = %q, want shown text", got)
	}
	// A leading "/" in displayText is a TUI rendering hint for a slash
	// command whose prompt was already injected into userText — the raw
	// command text must not override the actual model input.
	if got := DisplayTextOrUserText("expanded prompt", "/init"); got != "expanded prompt" {
		t.Fatalf("DisplayTextOrUserText with a slash rendering hint = %q, want the expanded userText", got)
	}
	// If userText is itself a slash command, the hint heuristic doesn't
	// apply — displayText wins as usual.
	if got := DisplayTextOrUserText("/init", "/init"); got != "/init" {
		t.Fatalf("DisplayTextOrUserText when both are slash commands = %q, want /init", got)
	}
}

type cancelStore struct {
	id     string
	status state.RunStatus
}

func (s *cancelStore) SetStatus(_ context.Context, id string, status state.RunStatus) error {
	s.id, s.status = id, status
	return nil
}

func (s *cancelStore) CancelRunningDescendants(_ context.Context, id string) error {
	s.id = id
	return nil
}

func TestFinalizeCancel(t *testing.T) {
	store := &cancelStore{}
	FinalizeCancel(context.Background(), store, " run ")
	require.Equal(t, "run", store.id)
	require.Equal(t, state.RunStatusCancelled, store.status)

	*store = cancelStore{}
	FinalizeCancel(context.Background(), store, "")
	require.Empty(t, store.id)
}

// pngTestHeader is the full 8-byte PNG signature, which is what content
// sniffing needs to recognize the file as an image.
var pngTestHeader = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

func TestClassify(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	textPath := write("a.go", []byte("package a\n"))
	imgPath := write("p.PNG", pngTestHeader)
	pdfPath := write("d.pdf", []byte("%PDF-1.4"))
	binPath := write("x.dat", []byte{0x00, 0x01, 0x02})
	// The attachment encoder rejects image/bmp, so routing it as an image
	// would fail the whole turn instead of ignoring one turn.
	bmpPath := write("shot.bmp", []byte("BM\x00\x00"))
	// Named like an image but is not one: codex probes the bytes at selection
	// time for the same reason, falling back to a plain path.
	fakePath := write("fake.png", []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"))
	emptyPath := write("empty.png", nil)

	cases := []struct {
		path string
		want Kind
	}{
		{textPath, KindOther},
		{imgPath, KindImage}, // extension match is case-insensitive
		{pdfPath, KindOther},
		{binPath, KindOther},
		{bmpPath, KindOther},
		{fakePath, KindOther},
		{emptyPath, KindOther},
		{dir, KindOther},
		{filepath.Join(dir, "missing.txt"), KindMissing},
	}
	for _, tc := range cases {
		if got := Classify(tc.path); got != tc.want {
			t.Errorf("Classify(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestTokenAtCursor(t *testing.T) {
	cases := []struct {
		name      string
		draft     string
		cursor    int
		wantStart int
		wantQuery string
		wantOK    bool
	}{
		{"at start", "@int", 4, 0, "int", true},
		{"after space", "see @src/m", 10, 4, "src/m", true},
		{"bare at", "@", 1, 0, "", true},
		{"mid-word at rejected", "user@host", 9, 0, "", false},
		{"space breaks token", "@a b", 4, 0, "", false},
		{"cursor inside token", "@abc", 2, 0, "a", true},
		{"open quote", `@"my do`, 7, 0, "my do", true},
		{"closed quote no menu", `@"a b"`, 6, 0, "", false},
		{"no at", "hello", 5, 0, "", false},
		{"disallowed rune", "@a,b", 4, 0, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, query, ok := TokenAtCursor(tc.draft, tc.cursor)
			if ok != tc.wantOK || (ok && (start != tc.wantStart || query != tc.wantQuery)) {
				t.Fatalf("got (%d,%q,%v), want (%d,%q,%v)", start, query, ok, tc.wantStart, tc.wantQuery, tc.wantOK)
			}
		})
	}
}

func TestReplacementDropsSigilForFilesAndKeepsItForDirs(t *testing.T) {
	cases := []struct {
		name string
		cand Candidate
		want string
	}{
		// "@" is a picker affordance, not prompt syntax, so a file resolves to
		// the bare path the user would have typed by hand.
		{"file", Candidate{Path: "main.go"}, "main.go "},
		{"file with spaces", Candidate{Path: "my docs/a b.txt"}, `"my docs/a b.txt" `},
		// A directory keeps the sigil so the token stays open for drill-down.
		{"dir", Candidate{Path: "internal", IsDir: true}, "@internal/"},
		{"dir with spaces", Candidate{Path: "my docs/", IsDir: true}, `@"my docs/`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Replacement(tc.cand); got != tc.want {
				t.Fatalf("Replacement(%#v) = %q, want %q", tc.cand, got, tc.want)
			}
		})
	}
}

func TestAcceptReplacesTokenInPlace(t *testing.T) {
	root := t.TempDir()
	draft := "explain @main.go to me"
	start, _, ok := TokenAtCursor(draft, len([]rune("explain @main.go")))
	if !ok {
		t.Fatal("token not found")
	}

	acc, ok := Accept(draft, start, len([]rune("explain @main.go")), Candidate{Path: "main.go"}, root)
	if !ok {
		t.Fatal("accept failed")
	}
	// Only the token is rewritten, and the cursor lands just past it rather
	// than jumping to the end of the draft.
	if acc.Draft != "explain main.go  to me" {
		t.Fatalf("draft = %q", acc.Draft)
	}
	if want := len([]rune("explain main.go ")); acc.Cursor != want {
		t.Fatalf("cursor = %d, want %d", acc.Cursor, want)
	}
	if acc.ImagePath != "" || acc.KeepOpen {
		t.Fatalf("unexpected routing: %#v", acc)
	}
}

func TestAcceptKeepsPickerOpenForDirectory(t *testing.T) {
	root := t.TempDir()
	acc, ok := Accept("@int", 0, 4, Candidate{Path: "internal", IsDir: true}, root)
	if !ok {
		t.Fatal("accept failed")
	}
	if acc.Draft != "@internal/" || !acc.KeepOpen {
		t.Fatalf("dir accept = %#v", acc)
	}
}

func TestAcceptAttachesImageInsteadOfNamingIt(t *testing.T) {
	root := t.TempDir()
	img := filepath.Join(root, "shot.png")
	if err := os.WriteFile(img, pngTestHeader, 0o644); err != nil {
		t.Fatal(err)
	}
	draft := "look at @shot.png"

	acc, ok := Accept(draft, 8, len([]rune(draft)), Candidate{Path: "shot.png"}, root)
	if !ok {
		t.Fatal("accept failed")
	}
	if acc.ImagePath != img {
		t.Fatalf("image path = %q, want %q", acc.ImagePath, img)
	}
	// The token is gone entirely: the image arrives as an attachment, so
	// naming it in the prompt too would be redundant.
	if acc.Draft != "look at " {
		t.Fatalf("draft = %q, want the token removed", acc.Draft)
	}
	if want := len([]rune("look at ")); acc.Cursor != want {
		t.Fatalf("cursor = %d, want %d", acc.Cursor, want)
	}
}

// A file merely named like an image is rejected by the attachment encoder, and
// that rejection would fail the whole turn, so it degrades to a plain path.
func TestAcceptDoesNotAttachMisnamedImage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fake.png"), []byte("<svg></svg>"), 0o644); err != nil {
		t.Fatal(err)
	}

	acc, ok := Accept("@fake.png", 0, 9, Candidate{Path: "fake.png"}, root)
	if !ok {
		t.Fatal("accept failed")
	}
	if acc.ImagePath != "" {
		t.Fatalf("misnamed image was attached: %q", acc.ImagePath)
	}
	if acc.Draft != "fake.png " {
		t.Fatalf("draft = %q", acc.Draft)
	}
}

func TestAcceptRejectsOutOfRangeToken(t *testing.T) {
	if _, ok := Accept("hi", 5, 7, Candidate{Path: "x"}, t.TempDir()); ok {
		t.Fatal("accepted a token outside the draft")
	}
}

// A selection made outside the composer (file tree, drag) inserts at the caret
// with no "@" token to replace, including at the very end of the draft.
func TestAcceptInsertsAtEmptyRange(t *testing.T) {
	root := t.TempDir()

	acc, ok := Accept("look at ", 8, 8, Candidate{Path: "main.go"}, root)
	if !ok {
		t.Fatal("accept failed at end of draft")
	}
	if acc.Draft != "look at main.go " {
		t.Fatalf("draft = %q", acc.Draft)
	}

	if _, ok := Accept("", 0, 0, Candidate{Path: "main.go"}, root); !ok {
		t.Fatal("accept failed on an empty draft")
	}
}

func TestDynamicCommandAppearsInDiscoveryAndExecutes(t *testing.T) {
	ResetDynamic()
	t.Cleanup(ResetDynamic)

	err := ReplaceDynamicSource("test-home", []DynamicCommand{
		{
			Command: Command{
				Name:               "opsx:apply",
				Description:        "apply openspec change",
				AllowedSurfaces:    []Surface{SurfaceWebChat, SurfaceTUI},
				SupportsInlineArgs: true,
				Visibility:         VisibilityPublic,
			},
			Handler: func(ctx Context, line string, toks []string) Result {
				desc := `target_skill: ""
slash_command: "opsx:apply"
args: "` + strings.Join(toks[1:], " ") + `"`
				return Result{
					Handled:           true,
					ShouldContinueRun: true,
					ContinueInput:     desc,
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("ReplaceDynamicSource: %v", err)
	}

	cmd, ok := Find("opsx:apply")
	if !ok {
		t.Fatalf("expected dynamic command to be registered")
	}
	if cmd.Name != "opsx:apply" {
		t.Fatalf("unexpected command: %+v", cmd)
	}

	list := Visible(SurfaceWebChat)
	found := false
	for _, it := range list {
		if it.Name == "opsx:apply" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected Visible to include dynamic command")
	}

	res := Execute(Context{Home: t.TempDir(), SessionID: "s1", Surface: SurfaceWebChat}, "/opsx:apply demo-change")
	if !res.Handled || !res.ShouldContinueRun {
		t.Fatalf("expected command to continue run, got %+v", res)
	}
	if !strings.Contains(res.ContinueInput, `target_skill: ""`) {
		t.Fatalf("expected structured continue input, got %q", res.ContinueInput)
	}
	if !strings.Contains(res.ContinueInput, `slash_command: "opsx:apply"`) {
		t.Fatalf("expected slash command in continue input, got %q", res.ContinueInput)
	}
	if !strings.Contains(res.ContinueInput, "demo-change") {
		t.Fatalf("expected args in continue input, got %q", res.ContinueInput)
	}
}

// A skill named like a built-in registers, but the built-in keeps its name:
// the skill is not a slash command (the /skills picker still runs it).
func TestReplaceDynamicSourceLeavesBuiltinNamesToBuiltins(t *testing.T) {
	ResetDynamic()
	t.Cleanup(ResetDynamic)

	err := ReplaceDynamicSource("test-home", []DynamicCommand{
		{
			Command: Command{
				Name:               "skills",
				Description:        "shadow",
				AllowedSurfaces:    []Surface{SurfaceWebChat},
				SupportsInlineArgs: true,
				Visibility:         VisibilityPublic,
			},
			Handler: func(ctx Context, line string, toks []string) Result {
				return Result{Handled: true, ShouldContinueRun: true, ContinueInput: "shadow-handler"}
			},
		},
	})
	if err != nil {
		t.Fatalf("a skill with a built-in's name must still register: %v", err)
	}
	if _, ok := findDynamic("skills"); ok {
		t.Fatal("a skill with a built-in's name became a slash command")
	}
	if cmd, ok := Find("skills"); !ok || cmd.Description == "shadow" {
		t.Fatalf("/skills = %+v, want the built-in", cmd)
	}
}

func TestReplaceDynamicSourceTolerantOfInvalidEntries(t *testing.T) {
	ResetDynamic()
	t.Cleanup(ResetDynamic)

	err := ReplaceDynamicSource("test-home", []DynamicCommand{
		{
			Command: Command{Name: "", Description: "missing name"},
			Handler: func(ctx Context, line string, toks []string) Result { return Result{Handled: true} },
		},
		{
			Command: Command{Name: "good", Description: "valid"},
			Handler: func(ctx Context, line string, toks []string) Result {
				return Result{Handled: true, ShouldContinueRun: true}
			},
		},
		{
			Command: Command{Name: "broken", Description: "no handler"},
			Handler: nil,
		},
	})
	if err == nil {
		t.Fatalf("expected non-nil aggregate error for invalid entries")
	}

	cmd, ok := Find("good")
	if !ok || cmd.Name != "good" {
		t.Fatalf("expected valid entry to register, got %+v ok=%v", cmd, ok)
	}
	if _, ok := Find("broken"); ok {
		t.Fatalf("expected handler-less entry to be dropped")
	}
}

func TestReplaceDynamicSourceRejectsRemovedSlashCommandNames(t *testing.T) {
	ResetDynamic()
	t.Cleanup(ResetDynamic)

	for _, removed := range []string{"copy", "side", "statusline", "title", "todo", "diff", "help"} {
		err := ReplaceDynamicSource("test-home-"+removed, []DynamicCommand{
			{
				Command: Command{
					Name:            removed,
					Description:     "removed command",
					AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI},
					Visibility:      VisibilityPublic,
				},
				Handler: func(ctx Context, line string, toks []string) Result { return Result{Handled: true} },
			},
			{
				Command: Command{
					Name:            "good-" + removed,
					Description:     "valid",
					AllowedSurfaces: []Surface{SurfaceWebChat},
					Visibility:      VisibilityPublic,
				},
				Handler: func(ctx Context, line string, toks []string) Result { return Result{Handled: true} },
			},
		})
		if err == nil {
			t.Fatalf("expected removed slash command name %q to be rejected", removed)
		}
		if _, ok := Find(removed); ok {
			t.Fatalf("removed slash command name %q must not be dynamically registered", removed)
		}
		if _, ok := Find("good-" + removed); !ok {
			t.Fatalf("valid dynamic command should still register alongside rejected %q", removed)
		}
	}
}

func TestDynamicCommandBareNameAccepted(t *testing.T) {
	ResetDynamic()
	t.Cleanup(ResetDynamic)

	err := ReplaceDynamicSource("test-home", []DynamicCommand{
		{
			Command: Command{
				Name:               "foo",
				Description:        "bare name",
				AllowedSurfaces:    []Surface{SurfaceWebChat},
				SupportsInlineArgs: true,
				Visibility:         VisibilityPublic,
			},
			Handler: func(ctx Context, line string, toks []string) Result {
				return Result{Handled: true, ShouldContinueRun: true}
			},
		},
	})
	if err != nil {
		t.Fatalf("expected bare name to be accepted, got %v", err)
	}
	cmd, ok := Find("foo")
	if !ok || cmd.Name != "foo" {
		t.Fatalf("expected bare-name dynamic command to be findable, got %+v ok=%v", cmd, ok)
	}
}

func TestExecuteDynamicOnlyRejectsBuiltinButRunsDynamic(t *testing.T) {
	ResetDynamic()
	t.Cleanup(ResetDynamic)

	res := ExecuteDynamicOnly(Context{}, "/plan")
	if !res.Handled {
		t.Fatalf("expected builtin to be blocked in dynamic-only mode, got %+v", res)
	}
	if res.Reply != "channel slash supports skill commands only" {
		t.Fatalf("unexpected builtin reply: %q", res.Reply)
	}

	err := ReplaceDynamicSource("test-home", []DynamicCommand{
		{
			Command: Command{
				Name:               "opsx:apply",
				Description:        "apply openspec change",
				AllowedSurfaces:    []Surface{SurfaceWebChat, SurfaceTUI},
				SupportsInlineArgs: true,
				Visibility:         VisibilityPublic,
			},
			Handler: func(ctx Context, line string, toks []string) Result {
				return Result{Handled: true, ShouldContinueRun: true, ContinueInput: strings.Join(toks[1:], " ")}
			},
		},
	})
	if err != nil {
		t.Fatalf("ReplaceDynamicSource: %v", err)
	}
	res = ExecuteDynamicOnly(Context{}, "/opsx:apply demo-change")
	if !res.Handled || !res.ShouldContinueRun || res.ContinueInput != "demo-change" {
		t.Fatalf("expected dynamic command to run in dynamic-only mode, got %+v", res)
	}
}

func TestFuzzyMatchSubsequence(t *testing.T) {
	cases := []struct {
		target, query string
		want          bool
	}{
		{"internal/cli/commands.go", "cmd", true},
		{"internal/cli/commands.go", "icc", true},
		{"internal/cli/commands.go", "xyz", false},
		{"foo", "foo", true},
		{"foo", "fooo", false},
		{"", "x", false},
		{"abc", "", true},
	}
	for _, tc := range cases {
		matched, _ := fuzzyMatch(tc.target, tc.query)
		if matched != tc.want {
			t.Errorf("fuzzyMatch(%q, %q)=%v want %v", tc.target, tc.query, matched, tc.want)
		}
	}
}

func TestFuzzyRankPrefersBoundaryAndConsecutive(t *testing.T) {
	options := []string{
		"docs/internal/cli/commands.md",
		"internal/cli/commands.go",
		"another/folder/cmd_helper.go",
	}
	ranked := fuzzyRank(options, "cmd")
	if len(ranked) < 2 {
		t.Fatalf("expected fuzzy matches, got %d", len(ranked))
	}
	first := options[ranked[0]]
	if first != "another/folder/cmd_helper.go" {
		t.Logf("fuzzy ranking: %v", func() []string {
			out := make([]string, len(ranked))
			for i, idx := range ranked {
				out[i] = options[idx]
			}
			return out
		}())
		t.Errorf("expected boundary 'cmd' hit first, got %q", first)
	}
}

func TestFuzzyRankExcludesNonMatches(t *testing.T) {
	options := []string{"foo.go", "bar.go", "baz.go"}
	ranked := fuzzyRank(options, "qq")
	if len(ranked) != 0 {
		t.Errorf("expected no matches for query 'qq', got %d", len(ranked))
	}
}

func TestMain(m *testing.M) {
	if cat, err := llm.Parse(llm.EmbeddedModelsJSON()); err == nil {
		llm.SetGlobalCatalogForTest(cat)
	}
	os.Exit(m.Run())
}

type stubPermissionFacade struct {
	snapshot safety.Snapshot
	decision safety.Decision
	explain  safety.ExplainResult
	updates  []safety.PermissionUpdate
}

func (s *stubPermissionFacade) PermissionSnapshot() safety.Snapshot {
	return s.snapshot
}

func (s *stubPermissionFacade) PermissionSnapshotForSession(string) safety.Snapshot {
	return s.snapshot
}

func (s *stubPermissionFacade) EvaluatePermission(toolName, input string) safety.Decision {
	return s.decision
}

func (s *stubPermissionFacade) EvaluatePermissionForSession(sessionID, toolName, input string) safety.Decision {
	return s.decision
}

func (s *stubPermissionFacade) ExplainPermission(toolName, input string) safety.ExplainResult {
	return s.explain
}

func (s *stubPermissionFacade) ExplainPermissionForSession(sessionID, toolName, input string) safety.ExplainResult {
	return s.explain
}

func (s *stubPermissionFacade) ApplyPermissionUpdate(update safety.PermissionUpdate) {
	s.updates = append(s.updates, update)
}

func TestPermissionFacadeDelegation(t *testing.T) {
	facade := &stubPermissionFacade{
		snapshot: safety.Snapshot{
			Mode: safety.ModeNever,
			Rules: map[safety.PermissionSource]map[safety.PermissionBehavior][]safety.PermissionRuleValue{
				safety.SourceSession: {
					safety.BehaviorAllow: {{
						ToolName:    "Bash",
						RuleContent: "git status",
					}},
				},
			},
		},
		decision: safety.Decision{
			Behavior: safety.BehaviorAllow,
			Mode:     safety.ModeNever,
			Reason:   "matched_rule",
		},
		explain: safety.ExplainResult{
			Decision: safety.Decision{
				Behavior: safety.BehaviorAllow,
				Mode:     safety.ModeNever,
				Reason:   "matched_rule",
			},
		},
	}
	svc := New(WithPermissionFacade(facade))

	snap := svc.PermissionSnapshot()
	require.Equal(t, safety.ModeNever, snap.Mode)
	require.Len(t, snap.Rules[safety.SourceSession][safety.BehaviorAllow], 1)

	dec := svc.EvaluatePermission("Bash", "git status")
	require.Equal(t, safety.BehaviorAllow, dec.Behavior)
	require.Equal(t, "matched_rule", dec.Reason)

	ex := svc.ExplainPermission("Bash", "git status")
	require.Equal(t, safety.BehaviorAllow, ex.Decision.Behavior)

	update := safety.PermissionUpdate{
		Type:        safety.UpdateSetMode,
		Destination: safety.DestinationSession,
		SessionID:   "session-1",
		Mode:        safety.ModeOnRequest,
	}
	svc.ApplyPermissionUpdate(update)
	require.Len(t, facade.updates, 1)
	require.Equal(t, update, facade.updates[0])
}

func TestBuildPromptDelimitsRequestAndPlan(t *testing.T) {
	prompt := BuildPrompt(Request{
		Task: "Add S3 sync to the skill installer.",
		Plan: "# Plan\n\n1. Do the thing.",
	})
	for _, want := range []string{
		"<request>\nAdd S3 sync to the skill installer.\n</request>",
		"<plan>\n# Plan\n\n1. Do the thing.\n</plan>",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

// The reviewer has investigation tools; the task must tell it to use them
// before judging, or it reviews the plan against the plan.
func TestBuildPromptOrdersInvestigationBeforeJudgement(t *testing.T) {
	prompt := BuildPrompt(Request{Task: "Add S3 sync.", Plan: "# Plan"})
	read := strings.Index(prompt, "Read the code this touches before you judge the plan")
	if read < 0 {
		t.Fatalf("task must direct the reviewer to the source first:\n%s", prompt)
	}
	if !strings.Contains(prompt, "work out for yourself what the request requires") {
		t.Fatalf("task must ask for the reviewer's own understanding:\n%s", prompt)
	}
}

func TestBuildPromptSaysWhenTheRequestIsUnavailable(t *testing.T) {
	prompt := BuildPrompt(Request{Plan: "# Plan"})
	if !strings.Contains(prompt, "the originating request is not available") &&
		!strings.Contains(prompt, "The originating request is not available") {
		t.Fatalf("prompt must admit the missing task context:\n%s", prompt)
	}
}

func TestBuildPromptTruncatesAnOversizedPlan(t *testing.T) {
	prompt := BuildPrompt(Request{Plan: strings.Repeat("x", MaxPlanChars+500)})
	if !strings.Contains(prompt, "[truncated]") {
		t.Fatal("an oversized plan must be marked as cut off, not silently shortened")
	}
	if len([]rune(prompt)) > MaxPlanChars+MaxTaskChars+400 {
		t.Fatalf("prompt is not bounded: %d runes", len([]rune(prompt)))
	}
}

func TestComposeDenyFeedbackWithoutReviewsIsTheUsersOwnWords(t *testing.T) {
	if got := ComposeDenyFeedback(nil, "  revise the API  "); got != "revise the API" {
		t.Fatalf("feedback = %q", got)
	}
	if got := ComposeDenyFeedback([]PlanReviewResult{{Text: "   "}}, ""); got != "" {
		t.Fatalf("an empty review must contribute nothing, got %q", got)
	}
}

func TestComposeDenyFeedbackCarriesReviewsAndRanksTheUserAbove(t *testing.T) {
	got := ComposeDenyFeedback([]PlanReviewResult{
		{Model: Model{Provider: "openai", Model: "gpt-5.1"}, Text: "Verdict: rework. No verification step."},
		{Model: Model{Provider: "anthropic", Model: "claude-opus-4-1"}, Text: "Verdict: approve with changes."},
	}, "I disagree about the verification step.")

	for _, want := range []string{
		"openai / gpt-5.1 and anthropic / claude-opus-4-1",
		`<review model="openai / gpt-5.1">`,
		"No verification step.",
		`<review model="anthropic / claude-opus-4-1">`,
		"The user's own feedback, which outranks the reviews:",
		"I disagree about the verification step.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("deny feedback missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "<review") > strings.Index(got, "outranks the reviews") {
		t.Fatalf("reviews must precede the user's feedback:\n%s", got)
	}
}

func TestComposeDenyFeedbackKeepsReviewWithoutUserFeedback(t *testing.T) {
	got := ComposeDenyFeedback([]PlanReviewResult{
		{Model: Model{Provider: "openai", Model: "gpt-5.1"}, Text: "Verdict: rework."},
	}, "")
	if !strings.Contains(got, "Verdict: rework.") {
		t.Fatalf("a review must reach the planner even when the user typed nothing:\n%s", got)
	}
	if strings.Contains(got, "outranks the reviews") {
		t.Fatalf("no user feedback section should be emitted:\n%s", got)
	}
}

func TestComposeDenyFeedbackTruncatesARamblingReview(t *testing.T) {
	got := ComposeDenyFeedback([]PlanReviewResult{{
		Model: Model{Provider: "openai", Model: "gpt-5.1"},
		Text:  strings.Repeat("y", MaxReviewChars+500),
	}}, "")
	if !strings.Contains(got, "[truncated]") {
		t.Fatal("an oversized review must be marked as cut off")
	}
}

func TestModelDisplayHandlesPartialIdentity(t *testing.T) {
	cases := map[Model]string{
		{Provider: "openai", Model: "gpt-5.1"}: "openai / gpt-5.1",
		{Model: "gpt-5.1"}:                     "gpt-5.1",
		{Provider: "openai"}:                   "openai",
		{}:                                     "",
	}
	for model, want := range cases {
		if got := model.Display(); got != want {
			t.Fatalf("Display(%#v) = %q, want %q", model, got, want)
		}
	}
}

// Per design doudou-main-design-20260528-121242.md §ArgumentHint registry pass.
// Every command with SupportsInlineArgs=true must have a non-empty, explicit
// ArgumentHint (no falling back to the generic "[args]" placeholder).
func TestAllInlineArgsCommandsHaveExplicitArgumentHint(t *testing.T) {
	t.Parallel()
	for _, cmd := range commands {
		if !cmd.SupportsInlineArgs {
			continue
		}
		hint := strings.TrimSpace(cmd.ArgumentHint)
		if hint == "" {
			t.Errorf("command /%s has SupportsInlineArgs=true but empty ArgumentHint", cmd.Name)
			continue
		}
		if hint == "[args]" {
			t.Errorf("command /%s ships generic [args] placeholder; expected specific hint", cmd.Name)
		}
	}
}

// subagents was previously SupportsInlineArgs=false despite paramCandidatesForSlash
// providing candidates (commands_catalog.go:123). The eng-review flip aligned the
// registry with the candidate provider so the inline-args picker can land on it.
func TestSubagentsHasNoInlineArgs(t *testing.T) {
	t.Parallel()
	for _, cmd := range commands {
		if cmd.Name != "subagents" {
			continue
		}
		// R-args: the subagent picker owns the choice; nothing here is
		// free-text, so the command takes no inline arguments.
		if cmd.SupportsInlineArgs {
			t.Fatalf("/subagents must have SupportsInlineArgs=false")
		}
		normalized := normalizeCommand(cmd)
		if normalized.Category != "agent" {
			t.Fatalf("/subagents category = %q, want \"agent\" (sibling of /agent)", normalized.Category)
		}
		return
	}
	t.Fatal("/subagents not found in registry")
}

func TestRemovedSlashCommandsAreNotRegistered(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"side", "title", "statusline", "copy", "todo", "board"} {
		if _, ok := Find(name); ok {
			t.Fatalf("removed slash command /%s must not be registered", name)
		}
		for _, cmd := range All() {
			if cmd.Name == name {
				t.Fatalf("removed slash command /%s must not appear in All()", name)
			}
		}
	}
}

// /exit closes the terminal app and is the terminal's alone; the web chat has
// nothing to exit and does not offer it.
func TestExitIsTheTerminalsAlone(t *testing.T) {
	t.Parallel()
	visible := func(surface Surface) bool {
		for _, cmd := range VisibleWithOptions(surface, DiscoveryOptions{FastAvailable: true}) {
			if cmd.Name == "exit" {
				return true
			}
		}
		return false
	}
	if !visible(SurfaceTUI) || visible(SurfaceWebChat) {
		t.Fatalf("/exit visible: tui=%v web=%v, want the terminal only", visible(SurfaceTUI), visible(SurfaceWebChat))
	}
	res := Execute(Context{Surface: SurfaceWebChat, SessionID: "s1"}, "/exit")
	if res.ExitRequested || res.Reply != "/exit is unavailable in the web chat." {
		t.Fatalf("/exit on the web = %+v", res)
	}
}

func TestSharedCommandsVisibleOnWebchatAndTUI(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"model"} {
		for _, surface := range []Surface{SurfaceWebChat, SurfaceTUI} {
			var found bool
			for _, cmd := range VisibleWithOptions(surface, DiscoveryOptions{FastAvailable: true}) {
				if cmd.Name == name {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("/%s must be visible on %s", name, surface)
			}
		}
	}
}

func TestCwdResolverJoinsRelativeAndAllowsAbsolute(t *testing.T) {
	dir := t.TempDir()
	r := CwdResolver{Dir: dir}

	abs, rel, ok := r.Resolve("sub/a.txt")
	if !ok || abs != filepath.Join(dir, "sub", "a.txt") || rel != "sub/a.txt" {
		t.Fatalf("relative: got %q %q %v", abs, rel, ok)
	}

	other := t.TempDir()
	target := filepath.Join(other, "b.txt")
	abs, rel, ok = r.Resolve(target)
	if !ok || abs != target || rel != filepath.ToSlash(target) {
		t.Fatalf("absolute: got %q %q %v", abs, rel, ok)
	}
}

func TestCwdResolverExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	r := CwdResolver{Dir: t.TempDir()}
	abs, _, ok := r.Resolve("~/x.txt")
	if !ok || abs != filepath.Join(home, "x.txt") {
		t.Fatalf("got %q %v", abs, ok)
	}
}

func TestWorkspaceResolverRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	r := WorkspaceResolver{Root: root}

	if _, _, ok := r.Resolve("../etc/passwd"); ok {
		t.Fatal("dotdot escape accepted")
	}
	if _, _, ok := r.Resolve("/etc/passwd"); ok {
		t.Fatal("absolute path accepted")
	}
	if _, _, ok := r.Resolve("~/x"); ok {
		t.Fatal("tilde accepted")
	}
	abs, rel, ok := r.Resolve("docs/readme.md")
	if !ok || abs != filepath.Join(root, "docs", "readme.md") || rel != "docs/readme.md" {
		t.Fatalf("inside: got %q %q %v", abs, rel, ok)
	}
}

func TestMinimalClearedResumeSnapshot(t *testing.T) {
	pending := llm.AssistantMessage(nil,
		llm.ToolCall{ID: "read", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "read_file", Arguments: `{}`}},
		llm.ToolCall{ID: "exit", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "exit_plan_mode", Arguments: `{}`}},
	)
	snapshot := []llm.Message{
		llm.SystemMessage("system"),
		llm.UserMessage(llm.Text("old history")),
		pending,
		llm.ToolResultMessage("read", llm.Text("done")),
	}
	got := MinimalClearedResumeSnapshot(snapshot, "exit_plan_mode")
	if len(got) != 4 || got[1].TextContent() != clearedResumeAnchor || !got[1].IsMeta || got[3].ToolCallID != "read" {
		t.Fatalf("snapshot=%#v", got)
	}
	if got := MinimalClearedResumeSnapshot(snapshot, "missing"); len(got) != len(snapshot) {
		t.Fatalf("missing call changed snapshot=%#v", got)
	}
}

// The gated assistant rides along in the cleared-context anchor for the model
// only — the live surface already rendered it before the clear — so it must
// carry the IsMeta marker a replay uses to skip rows no surface drew. Without
// it, a resume painted the same "calling <tool>" line twice: once for the
// pre-clear row and once for the anchor's copy.
func TestMinimalClearedResumeSnapshotMarksGatedAssistantMeta(t *testing.T) {
	pending := llm.AssistantMessage([]llm.ContentPart{llm.Text("calling exit_plan_mode")},
		llm.ToolCall{ID: "exit", Type: llm.ToolTypeFunction, Function: llm.FunctionCall{Name: "exit_plan_mode", Arguments: `{}`}},
	)
	snapshot := []llm.Message{
		llm.UserMessage(llm.Text("old history")),
		pending,
	}
	got := MinimalClearedResumeSnapshot(snapshot, "exit_plan_mode")
	if len(got) != 2 {
		t.Fatalf("snapshot=%#v", got)
	}
	if !got[1].IsMeta {
		t.Fatal("the anchor's copy of the gated assistant must be marked IsMeta so replays skip it")
	}
	if got[1].ToolCalls[0].ID != "exit" {
		t.Fatalf("the anchor must still carry the tool call the provider needs: %#v", got[1].ToolCalls)
	}
	// The snapshot the resume was handed is not mutated: the pre-clear copy
	// keeps its on-screen identity.
	if snapshot[1].IsMeta {
		t.Fatal("MinimalClearedResumeSnapshot mutated the caller's snapshot")
	}
}

func TestLastToolCallMessage(t *testing.T) {
	message, ok := LastToolCallMessage([]llm.Message{
		llm.AssistantMessage(nil, llm.ToolCall{ID: "first"}),
		llm.AssistantMessage(nil, llm.ToolCall{ID: "last"}),
	})
	if !ok || message.ToolCalls[0].ID != "last" {
		t.Fatalf("message=%#v ok=%t", message, ok)
	}
}

type stubChildRunStore struct {
	list []state.Run
}

func (s stubChildRunStore) ListChildRuns(ctx context.Context, parentRunID string, limit int, statuses ...state.RunStatus) ([]state.Run, error) {
	allowed := map[state.RunStatus]struct{}{}
	for _, status := range statuses {
		allowed[status] = struct{}{}
	}
	out := make([]state.Run, 0, len(s.list))
	for _, run := range s.list {
		if parentRunID != "" && run.ParentRunID != parentRunID {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[run.Status]; !ok {
				continue
			}
		}
		out = append(out, run)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func TestListSubagentHistoryUsesMergedHistory(t *testing.T) {
	workspace := t.TempDir()
	for _, entry := range []agent.HistoryEntry{
		{
			TaskID:      "task-1",
			RunID:       "child-1",
			ParentRunID: "parent-1",
			SessionID:   "session-1",
			Task:        "first",
			Status:      agent.StatusOK,
			Output:      "done",
			StartedAt:   10,
			UpdatedAt:   20,
			FinishedAt:  20,
		},
		{
			TaskID:      "task-2",
			RunID:       "child-2",
			ParentRunID: "parent-2",
			SessionID:   "session-2",
			Task:        "second",
			Status:      agent.StatusFailed,
			Error:       "boom",
			StartedAt:   11,
			UpdatedAt:   21,
			FinishedAt:  21,
		},
	} {
		require.NoError(t, agent.AppendHistory(workspace, entry))
	}

	svc := New(WithWorkspaceRoot(workspace))
	records, err := svc.ListSubagentHistory(SubagentHistoryQuery{
		SessionID: "session-1",
		Limit:     10,
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "task-1", records[0].TaskID)
	require.Equal(t, agent.StatusOK, records[0].Status)
}

func TestListRunSubagentsUsesChildRunStoreAndHistory(t *testing.T) {
	workspace := t.TempDir()
	for _, entry := range []agent.HistoryEntry{
		{
			TaskID:      "task-1",
			RunID:       "child-1",
			ParentRunID: "parent-1",
			SessionID:   "session-1",
			Task:        "first",
			Status:      agent.StatusRunning,
			StartedAt:   10,
			UpdatedAt:   10,
		},
		{
			TaskID:      "task-2",
			RunID:       "child-2",
			ParentRunID: "parent-1",
			SessionID:   "session-1",
			Task:        "second",
			Status:      agent.StatusFailed,
			Error:       "boom",
			StartedAt:   11,
			UpdatedAt:   21,
			FinishedAt:  21,
		},
	} {
		require.NoError(t, agent.AppendHistory(workspace, entry))
	}

	svc := New(nil,
		WithWorkspaceRoot(workspace),
		WithChildRunStore(stubChildRunStore{
			list: []state.Run{
				{ID: "child-1", ParentRunID: "parent-1", SessionID: "session-1", Status: state.RunStatusRunning, UpdatedAt: 20},
				{ID: "child-2", ParentRunID: "parent-1", SessionID: "session-1", Status: state.RunStatusDone, UpdatedAt: 15},
				{ID: "child-3", ParentRunID: "parent-2", SessionID: "session-2", Status: state.RunStatusRunning, UpdatedAt: 30},
			},
		}),
	)

	listing, err := svc.ListRunSubagents(context.Background(), "parent-1", 10, state.RunStatusRunning)
	require.NoError(t, err)
	require.Equal(t, "parent-1", listing.ParentRunID)
	require.Len(t, listing.Runs, 1)
	require.Equal(t, "child-1", listing.Runs[0].ID)
	require.Len(t, listing.Records, 2)
	require.Equal(t, "task-2", listing.Records[0].TaskID)
	require.Equal(t, "task-1", listing.Records[1].TaskID)
}

// Visible is the option-free form of the live VisibleWithOptions; only this
// package's tests called it.

func Visible(surface Surface) []Command {
	return VisibleWithOptions(surface, DiscoveryOptions{})
}

// A built-in always wins its name: Find, All and Execute all answer with the
// built-in, and skills are listed after every built-in, by name.
func TestBuiltinWinsItsNameOverASkill(t *testing.T) {
	ResetDynamic()
	t.Cleanup(ResetDynamic)
	skill := func(name, desc string) DynamicCommand {
		return DynamicCommand{
			Command: Command{Name: name, Description: desc, Category: SkillCategory, AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, Visibility: VisibilityPublic},
			Handler: func(ctx Context, line string, toks []string) Result {
				return Result{Handled: true, ShouldContinueRun: true, SkillName: name}
			},
		}
	}
	if err := ReplaceDynamicSource("test-home", []DynamicCommand{skill("zeta", "z"), skill("plan", "skill-plan"), skill("alpha", "a")}); err != nil {
		t.Fatalf("ReplaceDynamicSource: %v", err)
	}
	if cmd, ok := Find("plan"); !ok || cmd.Description == "skill-plan" {
		t.Fatalf("Find(plan) = %+v", cmd)
	}
	var names []string
	for _, cmd := range All() {
		if cmd.Name == "plan" && cmd.Description == "skill-plan" {
			t.Fatal("the plan skill is listed as a slash command")
		}
		if cmd.Category == SkillCategory {
			names = append(names, cmd.Name)
		}
	}
	if strings.Join(names, ",") != "alpha,zeta" {
		t.Fatalf("skills listed = %v, want alpha,zeta after the built-ins", names)
	}
	if all := All(); all[len(all)-1].Name != "zeta" || all[0].Category == SkillCategory {
		t.Fatalf("built-ins must come first: %v ... %v", all[0].Name, all[len(all)-1].Name)
	}
	if res := Execute(Context{Home: t.TempDir(), SessionID: "s1", Surface: SurfaceWebChat}, "/plan"); res.SkillName == "plan" {
		t.Fatalf("/plan ran the skill: %+v", res)
	}
}

// Every menu lists a filter's matches as two groups, built-in commands then
// skills, except that the group holding the best match leads.
func TestFilterGroupsCommandsAndSkillsBestMatchFirst(t *testing.T) {
	ResetDynamic()
	t.Cleanup(ResetDynamic)
	skill := func(name string) DynamicCommand {
		return DynamicCommand{
			Command: Command{Name: name, Description: name, Category: SkillCategory, AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, Visibility: VisibilityPublic},
			Handler: func(ctx Context, line string, toks []string) Result { return Result{Handled: true} },
		}
	}
	if err := ReplaceDynamicSource("test-home", []DynamicCommand{skill("mo"), skill("model-tuning")}); err != nil {
		t.Fatalf("ReplaceDynamicSource: %v", err)
	}
	kinds := func(cmds []Command) string {
		var out []string
		for _, cmd := range cmds {
			kind := "cmd"
			if cmd.Category == SkillCategory {
				kind = "skill"
			}
			out = append(out, kind+":"+cmd.Name)
		}
		return strings.Join(out, ",")
	}
	got := FilterWithOptions(SurfaceTUI, "mod", DiscoveryOptions{})
	if len(got) < 2 || got[0].Name != "model" || got[len(got)-1].Name != "model-tuning" {
		t.Fatalf("/mod lists %s, want the built-ins first and the skill last", kinds(got))
	}
	got = FilterWithOptions(SurfaceTUI, "mo", DiscoveryOptions{})
	if len(got) < 3 || got[0].Name != "mo" || got[1].Name != "model-tuning" || got[2].Category == SkillCategory {
		t.Fatalf("/mo lists %s, want the exact skill's group first, then the built-ins", kinds(got))
	}
}

// A command matches only when its name holds the whole query. "/connet" is a
// typo of /connect, and its letters also occur, scattered and in order, in a
// long skill name; neither is listed, because neither name contains "connet".
func TestFilterMatchesTheWholeQueryOnly(t *testing.T) {
	ResetDynamic()
	t.Cleanup(ResetDynamic)
	ci := DynamicCommand{
		Command: Command{Name: "golang-continuous-integration", Description: "CI", Category: SkillCategory, AllowedSurfaces: []Surface{SurfaceWebChat, SurfaceTUI}, Visibility: VisibilityPublic},
		Handler: func(ctx Context, line string, toks []string) Result { return Result{Handled: true} },
	}
	if err := ReplaceDynamicSource("test-home", []DynamicCommand{ci}); err != nil {
		t.Fatalf("ReplaceDynamicSource: %v", err)
	}
	if got := FilterWithOptions(SurfaceTUI, "connet", DiscoveryOptions{}); len(got) != 0 {
		t.Fatalf("/connet lists %v, want nothing", got)
	}
	got := FilterWithOptions(SurfaceTUI, "continuous", DiscoveryOptions{})
	if len(got) != 1 || got[0].Name != "golang-continuous-integration" {
		t.Fatalf("/continuous lists %v, want the skill whose name contains it", got)
	}
	got = FilterWithOptions(SurfaceTUI, "CONNECT", DiscoveryOptions{})
	if len(got) != 1 || got[0].Name != "connect" {
		t.Fatalf("/CONNECT lists %v, want /connect", got)
	}
}
