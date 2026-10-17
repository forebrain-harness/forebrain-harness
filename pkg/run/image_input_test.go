package run

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
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
	r := &Runner{Deps: &Deps{AppCfg: cfg, AgentName: "main"}}
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

// TestImageTurnRefusalJudgesTheSessionsOwnModel pins the third source of the
// session model: a web session may run on a model other than the runner's
// default, and the stored choice is what decides whether the request would
// carry an image into a model that cannot take one. A session with no stored
// choice follows the runner.
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
	cfg := newPrimaryModelRoot(
		primaryModelEntry("deepseek", "deepseek-chat", "k", "http://api.deepseek.test/v1", nil),
		primaryModelEntry("deepseek", "deepseek-v4-pro", "k", "http://api.deepseek.test/v1", nil),
	)
	r := &Runner{Deps: &Deps{AppCfg: cfg, AgentName: "main", SessionStore: sess}, FileResolver: PathResolver{}}
	if stored, err := sess.SaveSessionModelSelection(ctx, "s1", state.SessionModelSelection{Provider: "deepseek", Model: "deepseek-v4-pro"}); err != nil || !stored {
		t.Fatalf("save session model selection: stored=%v err=%v", stored, err)
	}

	err = ImageTurnRefusal(context.Background(), r, "s1", true)
	var refused *ImageInputRefusedError
	if !errors.As(err, &refused) || refused.Model != "deepseek-v4-pro" {
		t.Fatalf("a session pinned to a text-only model must be refused, got %v", err)
	}
	if err := ImageTurnRefusal(context.Background(), r, "s2", true); err != nil {
		t.Fatalf("a session with no stored choice follows the image-capable runner default, got %v", err)
	}
}
