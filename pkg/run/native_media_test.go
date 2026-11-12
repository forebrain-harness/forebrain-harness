package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	toolpkg "github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// The protocol a model's requests are sent on decides which kinds a model's own
// capability can actually turn into a part: it is the routing NewLLMFromYAML
// makes, asked here by a reader that has no client.
func TestProtocolForMediaFollowsTheRouting(t *testing.T) {
	cases := []struct{ provider, model, want string }{
		{"anthropic", "claude-x", llm.ProtocolMessages},
		{"minimax", "abab-x", llm.ProtocolMessages},
		{"chatgpt", "gpt-5", llm.ProtocolResponses},
		{"google", "gemini-3-pro", llm.ProtocolGemini},
		{"openai", "gpt-4o", llm.ProtocolChatCompletions},
		{"volcengine", "doubao-seed", llm.ProtocolChatCompletions},
		{"opencode", "gemini-3.5-pro", llm.ProtocolGemini},
		{"opencode", "claude-opus-4-7", llm.ProtocolMessages},
		{"opencode", "gpt-5.6-codex", llm.ProtocolResponses},
		{"opencode", "some-chat-model", llm.ProtocolChatCompletions},
	}
	for _, tc := range cases {
		if got := protocolForMedia(tc.provider, tc.model); got != tc.want {
			t.Errorf("protocolForMedia(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
	// A model the gateway refuses to route carries nothing: an unpublished
	// protocol is not a protocol.
	if got := protocolForMedia("opencode", "jev-decision"); got != "" {
		t.Errorf("protocolForMedia for an unroutable model = %q, want none", got)
	}
}

// mediaCaps is a configured row that switches every native kind on, the way a
// user's model settings can: what the catalog does not know, the row says.
func mediaCaps() *llm.ModelOverride {
	yes := true
	return &llm.ModelOverride{SupportsPDF: &yes, SupportsAudio: &yes, SupportsVideo: &yes}
}

// mediaTurnContext names the model and the row capabilities the assembly reads,
// the way a turn on a model with every native kind switched on does.
func mediaTurnContext(provider, model string, caps *llm.ModelOverride) context.Context {
	return WithSubagentModelOverride(context.Background(), SubagentModelOverride{
		Provider: provider, Model: model, Caps: caps,
	})
}

// TestNativeMediaKindsSubtractsTheKindThatWasRefused pins the read side: a kind
// this conversation's model already refused is not offered to the assembly
// again, the kinds it did not refuse are untouched, and a picture refusal (which
// has its own gate) never narrows this set.
func TestNativeMediaKindsSubtractsTheKindThatWasRefused(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	r := &Runner{Deps: &Deps{SessionStore: store}, FileResolver: PathResolver{}}
	const sid = "conv-media-kinds"
	if err := store.Ensure(ctx, sid, sid); err != nil {
		t.Fatalf("ensure session: %v", err)
	}

	// openai speaks chat completions and documents a PDF and audio: the model's
	// own switches decide the rest.
	openai := mediaTurnContext("openai", "gpt-main", mediaCaps())
	if got := NativeMediaKinds(openai, r, sid); !got.PDF || !got.Audio || got.Video {
		t.Fatalf("before any refusal = %+v, want pdf+audio", got)
	}

	// A picture refusal is ImageTurnRefusal's business: it must not take the
	// PDF away from a conversation that never had one refused.
	if err := store.RecordMediaRefusal(ctx, sid, "openai", "gpt-main", mediaKindImage, "unsupported image"); err != nil {
		t.Fatalf("record the picture refusal: %v", err)
	}
	if got := NativeMediaKinds(openai, r, sid); !got.PDF || !got.Audio {
		t.Fatalf("after the picture refusal = %+v, want pdf+audio untouched", got)
	}

	if err := store.RecordMediaRefusal(ctx, sid, "openai", "gpt-main", string(llm.MediaKindPDF), "unsupported content type"); err != nil {
		t.Fatalf("record the PDF refusal: %v", err)
	}
	got := NativeMediaKinds(openai, r, sid)
	if got.PDF {
		t.Fatalf("after the PDF refusal = %+v, want the PDF gone", got)
	}
	if !got.Audio {
		t.Fatalf("after the PDF refusal = %+v, want the audio still offered", got)
	}

	// A different model of the same provider learns nothing from this record.
	if got := NativeMediaKinds(mediaTurnContext("openai", "gpt-other", mediaCaps()), r, sid); !got.PDF {
		t.Fatalf("another model = %+v, want the PDF it never refused", got)
	}

	// The same conversation on a provider that documents audio and video: the
	// video refusal takes the video and nothing else.
	const volc = "conv-media-kinds-volcengine"
	if err := store.Ensure(ctx, volc, volc); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	volcengine := mediaTurnContext("volcengine", "doubao-seed", mediaCaps())
	if got := NativeMediaKinds(volcengine, r, volc); !got.Audio || !got.Video || got.PDF {
		t.Fatalf("volcengine before any refusal = %+v, want audio+video", got)
	}
	if err := store.RecordMediaRefusal(ctx, volc, "volcengine", "doubao-seed", string(llm.MediaKindVideo), "video_url is not enabled"); err != nil {
		t.Fatalf("record the video refusal: %v", err)
	}
	got = NativeMediaKinds(volcengine, r, volc)
	if got.Video || !got.Audio {
		t.Fatalf("after the video refusal = %+v, want the audio still offered", got)
	}
}

// TestNativeMediaKindsKeepsEveryKindWhenTheRecordCannotBeRead pins the failure
// path: a record that cannot be read is not a refusal, so every kind the model
// takes is still offered — a broken side table must never cost a turn the media
// it could have carried.
func TestNativeMediaKindsKeepsEveryKindWhenTheRecordCannotBeRead(t *testing.T) {
	ctx := context.Background()
	broken, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	store := state.NewSessionStore(broken, "main")
	if err := store.Ensure(ctx, "s", "s"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	if err := broken.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}
	r := &Runner{Deps: &Deps{SessionStore: store}, FileResolver: PathResolver{}}
	got := NativeMediaKinds(mediaTurnContext("openai", "gpt-main", mediaCaps()), r, "s")
	if !got.PDF || !got.Audio {
		t.Fatalf("a failed read = %+v, want every kind the model takes", got)
	}
}

// TestMediaRefusalRecorderRemembersTheKindTheProviderNamed pins the write side
// on its own: the observer files one row for the kind and the model that
// refused it, and leaves the picture row alone.
func TestMediaRefusalRecorderRemembersTheKindTheProviderNamed(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	const sid = "conv-media-recorder"
	if err := store.Ensure(ctx, sid, sid); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	r := &Runner{Deps: &Deps{SessionStore: store}, FileResolver: PathResolver{}}
	recorded := withMediaRefusalRecorder(mediaTurnContext("openai", "gpt-main", mediaCaps()), r, sid)
	observer := llm.MediaRefusalObserverFromContext(recorded)
	if observer == nil {
		t.Fatal("the run layer installed no media observer")
	}
	observer(recorded, llm.MediaKindPDF, "unsupported content type")

	reason, ok, err := store.MediaRefusal(ctx, sid, "openai", "gpt-main", string(llm.MediaKindPDF))
	if err != nil || !ok || reason != "unsupported content type" {
		t.Fatalf("recorded PDF: ok=%v reason=%q err=%v", ok, reason, err)
	}
	if _, ok, err := store.MediaRefusal(ctx, sid, "openai", "gpt-main", mediaKindImage); err != nil || ok {
		t.Fatalf("the picture row was written by a PDF refusal: ok=%v err=%v", ok, err)
	}
}

// refusingMediaProvider is a provider that refuses the request as a whole when
// it carries a native media part, the way an endpoint without that input does.
type refusingMediaProvider struct {
	mu    sync.Mutex
	calls int
	parts []int
}

func (p *refusingMediaProvider) Execute(_ context.Context, messages []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	carried := 0
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Type == llm.ContentTypeFileBase64 {
				carried++
			}
		}
	}
	p.mu.Lock()
	p.calls++
	p.parts = append(p.parts, carried)
	p.mu.Unlock()
	if carried > 0 {
		return nil, &llm.APIError{StatusCode: 400, Message: "unsupported content type: this model only accepts text"}
	}
	return &llm.Result{Message: &llm.Message{Role: llm.RoleAssistant, Parts: []llm.ContentPart{llm.Text("the summary")}}}, nil
}

// seedSessionWithPDF gives the conversation one user turn that carries a PDF, so
// the next request replays it exactly as a real attachment would.
func seedSessionWithPDF(t *testing.T, store *state.SessionStore, sessionID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.Ensure(ctx, sessionID, sessionID); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	pdf := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.7\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n"), 0o600); err != nil {
		t.Fatalf("write pdf fixture: %v", err)
	}
	text := "summarize this\n[File #1]"
	parts := state.MessagePartsJSON(llm.UserMessage(llm.Text(text)), text)
	parts = state.AppendRawPartJSON(parts, state.FileReferencePartJSON(pdf, "report.pdf", "application/pdf"))
	if _, err := store.AppendStructuredMessage(ctx, sessionID, "user", text, "", parts, "", "", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatalf("append user row: %v", err)
	}
}

// TestRunRemembersAProviderMediaRefusal pins the whole write path on a real
// turn: the provider refuses the PDF, exactly one request is made (a refused
// media part is not re-sent this turn, unlike an image), the kind is on record
// for the model that refused it, and the next assembly of that conversation no
// longer offers the PDF.
func TestRunRemembersAProviderMediaRefusal(t *testing.T) {
	ctx := context.Background()
	// The configured row switches every native kind on, the way the model page
	// does: capabilities are read per row, so the pair alone would take nothing.
	llm.SetModelOverrides(
		map[string]llm.ModelOverride{"row-media": *mediaCaps()},
		map[string]string{"row-media": llm.JoinProviderModel("openai", "gpt-main")},
	)
	t.Cleanup(func() { llm.SetModelOverrides(nil, nil) })
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	const sid = "conv-media-refusal"
	seedSessionWithPDF(t, store, sid)

	provider := &refusingMediaProvider{}
	r := mainModelRunner(t, store, provider, &collectingSink{})
	runID := "run-media-refusal"
	runCtx := toolpkg.WithRunID(llm.WithAgentSessionID(ctx, sid), runID)
	runCtx = toolpkg.WithConversationSessionID(runCtx, sid)
	if _, err := RunText(r, runCtx, "summarize this"); err == nil {
		t.Fatal("a provider that refuses the PDF must fail the turn")
	}
	provider.mu.Lock()
	calls, parts := provider.calls, append([]int(nil), provider.parts...)
	provider.mu.Unlock()
	if calls != 1 {
		t.Fatalf("requests = %d (%v), want exactly one — a refused media part is not re-sent this turn", calls, parts)
	}
	if len(parts) != 1 || parts[0] == 0 {
		t.Fatalf("parts per request = %v, want the refused request to have carried the PDF", parts)
	}

	reason, ok, err := store.MediaRefusal(ctx, sid, "openai", "gpt-main", string(llm.MediaKindPDF))
	if err != nil || !ok {
		t.Fatalf("the refusal was not remembered: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(reason, "unsupported content type") {
		t.Fatalf("reason = %q, want the provider's sentence", reason)
	}
	if _, ok, err := store.MediaRefusal(ctx, sid, "openai", "gpt-main", mediaKindImage); err != nil || ok {
		t.Fatalf("a PDF refusal wrote a picture row: ok=%v err=%v", ok, err)
	}

	// What the next turn is assembled from: the PDF is gone, the kinds the model
	// still takes are not.
	next := llm.WithAgentSessionID(ctx, sid)
	if got := NativeMediaKinds(next, r, sid); got.PDF || !got.Audio {
		t.Fatalf("next turn = %+v, want the PDF subtracted and the audio kept", got)
	}
}
