package run

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	appcfg "github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/event"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/state/dbx"
	toolpkg "github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

// withSessionModel carries a session model selection on the context, the way
// a turn does once a conversation has a stored (or explicit) choice.
func withSessionModel(provider, model string) context.Context {
	return WithSessionModelSelection(context.Background(), PrimaryModelState{Provider: provider, Model: model})
}

// TestImageInputRefusalRejectsTextOnlyModels pins the gate's reason to exist:
// a genuinely text-only model must answer with an actionable refusal (naming
// the model and pointing at /model) instead of the provider rejecting the turn
// with a 400 once the pixels are already sent.
func TestImageInputRefusalRejectsTextOnlyModels(t *testing.T) {
	// The real case owner confirmed on 2026-10-14: deepseek-v4-pro is a
	// text-only model that is selectable in his provider list. TestMain
	// installs the repository catalog, so this exercises the shipped data.
	got := (&Runner{}).ImageInputRefusal(withSessionModel("deepseek", "deepseek-v4-pro"))
	if got == "" {
		t.Fatal("expected a refusal for the text-only deepseek/deepseek-v4-pro")
	}
	if !strings.Contains(got, "deepseek-v4-pro") || !strings.Contains(got, "/model") {
		t.Fatalf("refusal=%q want it to name the model and point at /model", got)
	}

	// The same judgement on a synthetic text-only entry, so the assertion does
	// not rest on the contents of the data file. The repository catalog is
	// restored afterwards.
	previous, err := llm.RepoModelsJSONForTest()
	if err != nil {
		t.Fatalf("read repo models.json: %v", err)
	}
	synthetic, err := llm.Parse([]byte(`{"acme/text-only":{"id":"acme/text-only","attachment":false,"limit":{"context":1000,"output":100}}}`))
	if err != nil {
		t.Fatalf("parse synthetic catalog: %v", err)
	}
	llm.SetGlobalCatalogForTest(synthetic)
	t.Cleanup(func() {
		if restored, err := llm.Parse(previous); err == nil {
			llm.SetGlobalCatalogForTest(restored)
		}
	})
	if got := (&Runner{}).ImageInputRefusal(withSessionModel("acme", "text-only")); got == "" {
		t.Fatal("expected a refusal for a synthetic attachment:false model")
	}
}

// TestImageInputRefusalAllowsUnknownModels pins the fail-open rule: the
// catalog cannot tell a stale "no" from a correct one, so a model it does not
// know (a custom base_url with a custom name) is allowed through.
func TestImageInputRefusalAllowsUnknownModels(t *testing.T) {
	if got := (&Runner{}).ImageInputRefusal(withSessionModel("custom", "a-model-nobody-tabulated")); got != "" {
		t.Fatalf("unknown model must be allowed, got %q", got)
	}
}

// TestImageInputRefusalFallsBackToPrimaryModel pins the second source of the
// session model: when the context carries no selection (a session with no
// stored row, or a call outside a turn), the gate judges the runner's current
// selection.
func TestImageInputRefusalFallsBackToPrimaryModel(t *testing.T) {
	cfg := newPrimaryModelRoot(primaryModelEntry("deepseek", "deepseek-v4-pro", "k", "https://api.deepseek.com", nil))
	r := &Runner{Deps: (&Deps{AgentName: "main"}).WithConfig(cfg)}
	if got := r.ImageInputRefusal(context.Background()); got == "" {
		t.Fatal("expected the runner's configured text-only model to be judged without a context selection")
	}
}

// TestImageTurnRefusalCountsImagesAlreadyInTheConversation pins the turn gate:
// on a text-only model a turn is refused not only when the message attaches an
// image but also when the conversation already holds one (a message refused
// earlier and kept, or an image read before /model switched), because the
// request would replay it and the image is never dropped to let the model
// answer. A stored image whose file is gone is not counted: no request would
// carry it.
func TestImageTurnRefusalCountsImagesAlreadyInTheConversation(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sess := state.NewSessionStore(db, "main")
	if err := sess.Ensure(ctx, "s1", "s1"); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	r := &Runner{Deps: &Deps{SessionStore: sess}, FileResolver: PathResolver{}}
	textOnly := withSessionModel("deepseek", "deepseek-v4-pro")

	err = ImageTurnRefusal(textOnly, r, "s1", true)
	var refused *ImageInputRefusedError
	if !errors.As(err, &refused) || refused.Provider != "deepseek" || refused.Model != "deepseek-v4-pro" || !strings.Contains(err.Error(), "/model") {
		t.Fatalf("attached image on a text-only model: err=%v, want it to name the model and point at /model", err)
	}
	if err := ImageTurnRefusal(textOnly, r, "s1", false); err != nil {
		t.Fatalf("no image anywhere must not be refused, got %v", err)
	}

	img := filepath.Join(t.TempDir(), "clip.png")
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	if err := os.WriteFile(img, data, 0o600); err != nil {
		t.Fatalf("write png: %v", err)
	}
	text := "what is this\n[Image #1]"
	parts := state.MessagePartsJSON(llm.UserMessage(llm.Text(text)), text)
	parts = state.AppendRawPartJSON(parts, state.FileReferencePartJSON(img, "clip.png", "image/png"))
	if _, err := sess.AppendStructuredMessage(ctx, "s1", "user", text, "", parts, "", "", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatalf("append user row: %v", err)
	}

	if err := ImageTurnRefusal(textOnly, r, "s1", false); err == nil {
		t.Fatal("a stored image must refuse the next turn on a text-only model")
	}
	if err := ImageTurnRefusal(withSessionModel("custom", "a-model-nobody-tabulated"), r, "s1", false); err != nil {
		t.Fatalf("a model that may take images must not be refused, got %v", err)
	}

	if err := os.Remove(img); err != nil {
		t.Fatalf("remove png: %v", err)
	}
	if err := ImageTurnRefusal(textOnly, r, "s1", false); err != nil {
		t.Fatalf("an image no request can carry must not refuse the turn, got %v", err)
	}
}

// TestImageTurnRefusalFailsOpenOnARowThatSaysNothingAboutImages pins the gate's
// half of the capability-data contract the subscription fix relies on (owner
// report 2026-11-13). A configured row whose cap_images is NULL names no
// capability at all — state.ModelOverrideFromCaps answers ok=false for it — so
// the catalog alone answers, and a model nobody characterized is let through.
// A row that does say cap_images=0 is an answer: that turn is refused with the
// code a surface renders in the viewer's language.
func TestImageTurnRefusalFailsOpenOnARowThatSaysNothingAboutImages(t *testing.T) {
	const provider, model = "volcengine", "kimi-k3.1-pro"
	ctx := withSessionModel(provider, model)

	// Nothing installed: the row's columns are empty and no catalog entry
	// exists for the model, so the request may carry the image.
	llm.SetModelOverrides(nil, nil)
	t.Cleanup(func() { llm.SetModelOverrides(nil, nil) })
	if err := ImageTurnRefusal(context.Background(), &Runner{}, "", true); err != nil {
		t.Fatalf("an uncharacterized model must be let through, got %v", err)
	}
	if err := ImageTurnRefusal(ctx, &Runner{}, "", true); err != nil {
		t.Fatalf("a row that says nothing about images must be let through, got %v", err)
	}

	// The answer a fabricated row used to carry: cap_images=0.
	images := false
	llm.SetModelOverrides(
		map[string]llm.ModelOverride{"row-text-only": {SupportsImages: &images}},
		map[string]string{"row-text-only": llm.JoinProviderModel(provider, model)},
	)
	err := ImageTurnRefusal(ctx, &Runner{}, "", true)
	var refused *ImageInputRefusedError
	if !errors.As(err, &refused) || refused.Code() != ImageInputRefusedCode {
		t.Fatalf("a row saying no images must refuse the turn with %s, got %v", ImageInputRefusedCode, err)
	}
}

// TestImageTurnRefusalJudgesARowLessSessionsModel pins the gate to the reading
// every surface shows for a conversation that has not run yet: its first turn
// adopts the agent's most recently used model (RowLessSessionModel /
// NewSessionModel), so that is the model the image gate must judge — not the
// configuration default the runner happens to carry. Judging the default
// refused an image the turn would have accepted (the owner's report, 2026-11-14:
// a fresh session showed the multimodal model in the footer and refused the
// image in deepseek/deepseek-chat's name) and, the other way round, would have
// sent images to a text-only model whose row the first turn was about to adopt.
func TestImageTurnRefusalJudgesARowLessSessionsModel(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sess := state.NewSessionStore(db, "main")
	for _, sid := range []string{"older", "fresh"} {
		if err := sess.Ensure(ctx, sid, sid); err != nil {
			t.Fatalf("ensure session %s: %v", sid, err)
		}
	}
	// Both entries are configured; which one the fresh session runs on comes
	// from the most recently used model alone.
	cfg := newPrimaryModelRoot(
		primaryModelEntry("deepseek", "deepseek-v4-pro", "k", "http://api.deepseek.test/v1", nil),
		primaryModelEntry("deepseek", "deepseek-v4-flash", "k", "http://api.deepseek.test/v1", nil),
	)
	r := &Runner{Deps: (&Deps{AgentName: "main", SessionStore: sess}).WithConfig(cfg), FileResolver: PathResolver{}}

	// No stored row on the fresh session: the gate must judge what its first
	// turn will adopt. An image-capable last used model lets the turn proceed.
	if _, err := sess.SaveSessionModelSelection(ctx, "older", state.SessionModelSelection{Provider: "deepseek", Model: "deepseek-v4-flash"}); err != nil {
		t.Fatalf("save the last used model: %v", err)
	}
	if sel := NewSessionModel(r, sess); !sel.Set || sel.Model != "deepseek-v4-flash" {
		t.Fatalf("premise: the fresh session adopts %+v, want deepseek-v4-flash", sel)
	}
	if err := ImageTurnRefusal(ctx, r, "fresh", true); err != nil {
		t.Fatalf("the gate must judge the model the first turn adopts (an image-capable one), got %v", err)
	}

	// The same shape with a text-only last used model: the gate refuses, and
	// names the model the turn would have run on.
	if _, err := sess.SaveSessionModelSelection(ctx, "older", state.SessionModelSelection{Provider: "deepseek", Model: "deepseek-v4-pro"}); err != nil {
		t.Fatalf("save the text-only last used model: %v", err)
	}
	if sel := NewSessionModel(r, sess); !sel.Set || sel.Model != "deepseek-v4-pro" {
		t.Fatalf("premise: the fresh session adopts %+v, want deepseek-v4-pro", sel)
	}
	err = ImageTurnRefusal(ctx, r, "fresh", true)
	var refused *ImageInputRefusedError
	if !errors.As(err, &refused) || refused.Model != "deepseek-v4-pro" {
		t.Fatalf("the gate must refuse in the adopted model's name, got %v", err)
	}
}

// TestImageTurnRefusalJudgesTheSessionsOwnModel pins the third source of the
// session model: a web session may run on a model other than the runner's
// default, and the stored choice is what decides whether the request would
// carry an image into a model that cannot take one. A session with no stored
// choice follows the rule its turn entry does — a conversation that has not run
// adopts the last used model (pinned by
// TestImageTurnRefusalJudgesARowLessSessionsModel), an older one the configured
// default.
func TestImageTurnRefusalJudgesTheSessionsOwnModel(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sess := state.NewSessionStore(db, "main")
	for _, sid := range []string{"s1", "s2"} {
		if err := sess.Ensure(ctx, sid, sid); err != nil {
			t.Fatalf("ensure session %s: %v", sid, err)
		}
	}
	// s2 is a conversation older than the model-state table: it has run, and
	// holds no row saying what it runs on.
	if _, err := sess.AppendStructuredMessage(ctx, "s2", "assistant", "an answer", "", "", "", "", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatalf("give s2 a turn of its own: %v", err)
	}
	cfg := newPrimaryModelRoot(
		// The runner default has to be a model the catalog knows takes images:
		// deepseek-v4-flash is that family's multimodal member (the shipped
		// override pins it, owner 2026-10-14), while deepseek-chat and
		// deepseek-v4-pro are text-only since the 2026-10 upstream correction.
		primaryModelEntry("deepseek", "deepseek-v4-flash", "k", "http://api.deepseek.test/v1", nil),
		primaryModelEntry("deepseek", "deepseek-v4-pro", "k", "http://api.deepseek.test/v1", nil),
	)
	r := &Runner{Deps: (&Deps{AgentName: "main", SessionStore: sess}).WithConfig(cfg), FileResolver: PathResolver{}}
	if stored, err := sess.SaveSessionModelSelection(ctx, "s1", state.SessionModelSelection{Provider: "deepseek", Model: "deepseek-v4-pro"}); err != nil || !stored {
		t.Fatalf("save session model selection: stored=%v err=%v", stored, err)
	}

	err = ImageTurnRefusal(context.Background(), r, "s1", true)
	var refused *ImageInputRefusedError
	if !errors.As(err, &refused) || refused.Model != "deepseek-v4-pro" {
		t.Fatalf("a session pinned to a text-only model must be refused, got %v", err)
	}
	if err := ImageTurnRefusal(context.Background(), r, "s2", true); err != nil {
		t.Fatalf("a row-less session that has run follows the image-capable configured default, got %v", err)
	}
}

// imageRefusingLLM is a provider that will not take an image: any request
// carrying one is refused with the sentence a real provider sent (recorded
// verbatim in pkg/llm/image_heal_test.go), and any request without one answers
// with text. It keeps what each call carried so a test can say what the heal
// did.
type imageRefusingLLM struct {
	mu     sync.Mutex
	images []int
}

const imageRefusalSentence = ".messages[3].image[0]: You have uploaded an unsupported image. " +
	"Please make sure your image is valid and has one of the following formats: webp, png, jpeg, and gif."

func (l *imageRefusingLLM) Execute(_ context.Context, msgs []llm.Message, _ []*llm.Tool) (*llm.Result, error) {
	images := 0
	for _, msg := range msgs {
		for _, part := range msg.Parts {
			if part.Type == llm.ContentTypeImageBase64 || part.Type == llm.ContentTypeImageURL {
				images++
			}
		}
	}
	l.mu.Lock()
	l.images = append(l.images, images)
	l.mu.Unlock()
	if images > 0 {
		return nil, &llm.APIError{StatusCode: 400, Message: imageRefusalSentence}
	}
	msg := llm.AssistantMessage([]llm.ContentPart{llm.Text("I answered without the picture.")})
	return &llm.Result{Message: &msg}, nil
}

func (l *imageRefusingLLM) callImages() []int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]int(nil), l.images...)
}

// seedSessionWithImage stores one user row carrying a real image, the way a
// surface stores an attachment: the pixels live in a file the row references,
// and every later request rehydrates them from there.
func seedSessionWithImage(t *testing.T, store *state.SessionStore, sessionID string) {
	t.Helper()
	ctx := context.Background()
	if err := store.Ensure(ctx, sessionID, sessionID); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	img := filepath.Join(t.TempDir(), "shot.png")
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode png fixture: %v", err)
	}
	if err := os.WriteFile(img, data, 0o600); err != nil {
		t.Fatalf("write png fixture: %v", err)
	}
	text := "what is this\n[Image #1]"
	parts := state.MessagePartsJSON(llm.UserMessage(llm.Text(text)), text)
	parts = state.AppendRawPartJSON(parts, state.FileReferencePartJSON(img, "shot.png", "image/png"))
	if _, err := store.AppendStructuredMessage(ctx, sessionID, "user", text, "", parts, "", "", "", "", state.MessageExecTiming{}); err != nil {
		t.Fatalf("append user row: %v", err)
	}
}

// collectingSink keeps every event a run published, in order.
type collectingSink struct {
	mu     sync.Mutex
	events []event.RunEvent
}

func (s *collectingSink) Publish(_ context.Context, evt event.RunEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, evt)
	return nil
}

func (s *collectingSink) byType(typ string) []event.RunEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []event.RunEvent
	for _, evt := range s.events {
		if evt.Type == typ {
			out = append(out, evt)
		}
	}
	return out
}

// mainModelRunner builds a loaded runner whose provider is client, on a config
// naming one primary model, with the session/event plumbing the recorder needs.
func mainModelRunner(t *testing.T, store *state.SessionStore, client llm.LLM, sink event.Sink) *Runner {
	t.Helper()
	restore := SetLLMOverrideForTest(client)
	t.Cleanup(restore)
	cfg := &appcfg.Root{LLMModels: modelRowsForTest(appcfg.AgentLLMProviderConfig{Provider: "openai", Model: "gpt-main", APIKey: "k", BaseURL: "http://127.0.0.1:9/v1"}), Agents: appcfg.AgentsSection{Definitions: map[string]appcfg.AgentDefinition{
		"main": {Primary: true, DefaultModel: "llm-1"},
	}}}
	r := &Runner{Deps: (&Deps{Home: t.TempDir(), AgentName: "main", SessionStore: store}).WithConfig(cfg), Events: sink, FileResolver: PathResolver{}}
	if err := r.Load(); err != nil {
		t.Fatalf("load runner: %v", err)
	}
	return r
}

// TestRunRemembersAndReportsAProviderImageRefusal pins both halves of what the
// heal learns: the refusal is remembered for this conversation and the model that
// produced it, and it is published once, under an id derived from the run, so the
// surfaces can say what happened. Without the first half the conversation pays
// for the same refusal on every turn; without the second nobody is told why the
// answer ignored the picture.
func TestRunRemembersAndReportsAProviderImageRefusal(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	const sid = "conv-image-refusal"
	seedSessionWithImage(t, store, sid)

	provider := &imageRefusingLLM{}
	sink := &collectingSink{}
	r := mainModelRunner(t, store, provider, sink)

	runID := "run-image-refusal"
	runCtx := toolpkg.WithRunID(llm.WithAgentSessionID(ctx, sid), runID)
	runCtx = toolpkg.WithConversationSessionID(runCtx, sid)
	if _, err := RunText(r, runCtx, "and what about the header?"); err != nil {
		t.Fatalf("the turn failed: %v", err)
	}

	// The heal did its job: the first request carried the image and was
	// refused, the retry went without it.
	images := provider.callImages()
	if len(images) < 2 || images[0] == 0 || images[1] != 0 {
		t.Fatalf("images per request = %v, want the first carrying one and the retry carrying none", images)
	}

	// (a) The refusal is on record for the model in force.
	reason, recorded, err := store.MediaRefusal(ctx, sid, "openai", "gpt-main", "image")
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	if !recorded || !strings.Contains(reason, "unsupported image") {
		t.Fatalf("recorded=%v reason=%q, want the provider's sentence", recorded, reason)
	}

	// (b) Exactly one event, under the run's own id.
	reported := sink.byType(event.RunEventImageRejected)
	if len(reported) != 1 {
		t.Fatalf("%d image_rejected events, want one: %+v", len(reported), reported)
	}
	if want := "image-rejected:" + runID; reported[0].ID != want {
		t.Fatalf("event id = %q, want %q — the fixed id is what makes a repeated report one line", reported[0].ID, want)
	}
	if reported[0].RunID != runID || reported[0].SessionID != sid {
		t.Fatalf("event run/session = %q/%q", reported[0].RunID, reported[0].SessionID)
	}
	var payload event.ImageRejectedPayload
	if err := json.Unmarshal(reported[0].Payload, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.Code != ImageRejectedCode || payload.Provider != "openai" || payload.Model != "gpt-main" {
		t.Fatalf("payload = %+v, want the code and the model that refused", payload)
	}
	if !strings.Contains(payload.Reason, "unsupported image") {
		t.Fatalf("payload reason = %q", payload.Reason)
	}

	// The record is what the next turn is withheld on.
	refusal := ImageTurnRefusal(ctx, r, sid, false)
	var refused *ImageInputRefusedError
	if !errors.As(refusal, &refused) || !refused.Observed || refused.Reason != reason {
		t.Fatalf("next turn: err=%v, want the remembered refusal with its reason", refusal)
	}
	if refused.Code() != ImageRejectedCode || !strings.Contains(refused.Error(), "/model") || !strings.Contains(refused.Error(), reason) {
		t.Fatalf("refusal sentence/code: %q / %q", refused.Error(), refused.Code())
	}
}

// TestImageRefusalRecorderIsIdempotent pins the other half of the fixed id: a
// turn that meets several refusals reports them under one id, so the store's
// conflict drops the repeats and a surface that sees two of them draws one line.
func TestImageRefusalRecorderIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	const sid = "conv-image-idempotent"
	if err := store.Ensure(ctx, sid, sid); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	sink := &collectingSink{}
	r := mainModelRunner(t, store, &imageRefusingLLM{}, sink)

	runID := "run-image-idempotent"
	runCtx := toolpkg.WithRunID(llm.WithAgentSessionID(ctx, sid), runID)
	recorded := withImageRefusalRecorder(runCtx, r, sid)
	observer := llm.ImageRefusalObserverFromContext(recorded)
	if observer == nil {
		t.Fatal("the run layer installed no observer")
	}
	// A tool loop can meet several refusals in one turn.
	observer(runCtx, "first refusal")
	observer(runCtx, "second refusal")

	reported := sink.byType(event.RunEventImageRejected)
	if len(reported) != 2 {
		t.Fatalf("%d events, want both attempts published", len(reported))
	}
	if reported[0].ID != reported[1].ID {
		t.Fatalf("ids differ (%q vs %q) — a repeat would draw a second line", reported[0].ID, reported[1].ID)
	}
	// The row is one, refreshed: (session_id, provider, model) is the key.
	var rows int
	if err := db.QueryRowContext(ctx, dbx.Rebind(dbx.DialectOf(db),
		`SELECT COUNT(*) FROM fb_session_image_refusals WHERE session_id=?`), sid).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want one per (session, provider, model)", rows)
	}
	if reason, ok, err := store.MediaRefusal(ctx, sid, "openai", "gpt-main", "image"); err != nil || !ok || reason != "second refusal" {
		t.Fatalf("record: ok=%v reason=%q err=%v, want the latest sentence", ok, reason, err)
	}
}

// TestImageTurnRefusalRemembersWithoutBeingATextOnlyModel pins the four edges of
// the remembered refusal: it withholds a turn whose request would carry an image,
// never a text-only turn, never another model, and it says which of the two
// reasons it is.
func TestImageTurnRefusalRemembersWithoutBeingATextOnlyModel(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	const sid = "conv-remembered-gate"
	seedSessionWithImage(t, store, sid)
	r := &Runner{Deps: &Deps{SessionStore: store}, FileResolver: PathResolver{}}

	// A model the catalog does not know is the fail-open case, so anything the
	// gate does here comes from the record alone.
	imageModel := withSessionModel("acme", "vision-model")
	if err := ImageTurnRefusal(imageModel, r, sid, true); err != nil {
		t.Fatalf("nothing recorded yet: %v", err)
	}
	if err := store.RecordMediaRefusal(ctx, sid, "acme", "vision-model", "image", "unsupported image"); err != nil {
		t.Fatalf("record: %v", err)
	}

	// The request would carry an image: attached now, or already in the history.
	for _, attaches := range []bool{true, false} {
		err := ImageTurnRefusal(imageModel, r, sid, attaches)
		var refused *ImageInputRefusedError
		if !errors.As(err, &refused) || !refused.Observed || refused.Reason != "unsupported image" {
			t.Fatalf("attaches=%v: err=%v, want the remembered refusal", attaches, err)
		}
		if refused.Code() != ImageRejectedCode {
			t.Fatalf("attaches=%v: code=%q", attaches, refused.Code())
		}
	}

	// A text-only turn is never withheld: the record is about pictures.
	plain := &Runner{Deps: &Deps{SessionStore: store}, FileResolver: PathResolver{}}
	if err := store.Ensure(ctx, "conv-text-only", "conv-text-only"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if err := ImageTurnRefusal(imageModel, plain, "conv-text-only", false); err != nil {
		t.Fatalf("a text-only turn must never be withheld: %v", err)
	}

	// Switching model is the recovery: the record does not follow it.
	switched := withSessionModel("acme", "another-model")
	if err := ImageTurnRefusal(switched, r, sid, true); err != nil {
		t.Fatalf("another model must be free: %v", err)
	}

	// The catalog's own answer keeps its code: a text-only model is a different
	// fact from a remembered rejection.
	catalog := withSessionModel("deepseek", "deepseek-v4-pro")
	err = ImageTurnRefusal(catalog, r, sid, true)
	var catalogRefused *ImageInputRefusedError
	if !errors.As(err, &catalogRefused) {
		t.Fatalf("the catalog's refusal is still a refusal: %v", err)
	}
	if catalogRefused.Observed || catalogRefused.Code() != ImageInputRefusedCode {
		t.Fatalf("catalog refusal: observed=%v code=%q", catalogRefused.Observed, catalogRefused.Code())
	}
}

// A subagent's requests go to its own model, so a provider refusing an image
// on one is that model's refusal. Recording it against the conversation's
// model would withhold the conversation's next turn for a model that never
// refused anything.
func TestImageRefusalRecorderRecordsTheSubagentsOwnModel(t *testing.T) {
	ctx := context.Background()
	db, err := state.OpenStateForTest(ctx, filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := state.NewSessionStore(db, "main")
	const sid = "conv-subagent-refusal"
	if err := store.Ensure(ctx, sid, sid); err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	r := mainModelRunner(t, store, &imageRefusingLLM{}, &collectingSink{})

	reviewCtx := WithSubagentModelOverride(llm.WithAgentSessionID(ctx, sid), SubagentModelOverride{Provider: "acme", Model: "reviewer"})
	recorded := withImageRefusalRecorder(reviewCtx, r, sid)
	llm.ImageRefusalObserverFromContext(recorded)(reviewCtx, "no images here")

	if reason, ok, err := store.MediaRefusal(ctx, sid, "acme", "reviewer", "image"); err != nil || !ok || reason != "no images here" {
		t.Fatalf("reviewer record: ok=%v reason=%q err=%v", ok, reason, err)
	}
	if _, ok, err := store.MediaRefusal(ctx, sid, "openai", "gpt-main", "image"); err != nil || ok {
		t.Fatalf("the conversation's model was recorded as refusing: ok=%v err=%v", ok, err)
	}
}
