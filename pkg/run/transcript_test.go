package run

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
	"github.com/stretchr/testify/require"
)

func TestBuildTranscriptSessionUsesTranscriptActiveView(t *testing.T) {
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sess := state.NewSessionStore(db, "main")
	require.NotNil(t, sess)
	ctx := context.Background()
	require.NoError(t, sess.Ensure(ctx, "s1", "s1"))
	_, err = sess.AppendStructuredMessage(ctx, "s1", "user", "hello", "", state.MessagePartsJSON(llm.UserMessage(llm.Text("hello")), "hello"), "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)
	_, err = sess.AppendStructuredMessage(ctx, "s1", "assistant", "hi", "", state.MessagePartsJSON(llm.AssistantMessage([]llm.ContentPart{llm.Text("hi")}), "hi"), "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)
	_, err = sess.AppendStructuredMessage(ctx, "s1", "user", "next", "", state.MessagePartsJSON(llm.UserMessage(llm.Text("next")), "next"), "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)

	builder := transcriptSession{store: sess, systemPrompt: "sys", resolver: nil, projectInstructions: nil}.build
	msgs, _, err := builder(llm.WithAgentSessionID(ctx, "s1"), []llm.ContentPart{llm.Text("next")})
	require.NoError(t, err)
	require.Len(t, msgs, 4)
	require.Equal(t, llm.RoleSystem, msgs[0].Role)
	require.Equal(t, "sys", msgs[0].TextContent())
	require.Equal(t, llm.RoleUser, msgs[1].Role)
	require.Equal(t, "hello", msgs[1].TextContent())
	require.Equal(t, llm.RoleAssistant, msgs[2].Role)
	require.Equal(t, "hi", msgs[2].TextContent())
	require.Equal(t, llm.RoleUser, msgs[3].Role)
	require.Equal(t, "next", msgs[3].TextContent())
}

func TestBuildTranscriptSessionAppendsPendingUserWhenNotYetPersisted(t *testing.T) {
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sess := state.NewSessionStore(db, "main")
	require.NotNil(t, sess)
	ctx := llm.WithAgentSessionID(context.Background(), "s1")
	require.NoError(t, sess.Ensure(ctx, "s1", "s1"))
	_, err = sess.AppendStructuredMessage(ctx, "s1", "user", "hello", "", state.MessagePartsJSON(llm.UserMessage(llm.Text("hello")), "hello"), "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)

	builder := transcriptSession{store: sess, systemPrompt: "sys", resolver: nil, projectInstructions: nil}.build
	msgs, _, err := builder(ctx, []llm.ContentPart{llm.Text("pending")})
	require.NoError(t, err)
	require.Len(t, msgs, 3)
	require.Equal(t, "pending", msgs[2].TextContent())
}

func TestBuildTranscriptSessionIncludesSystemAddendum(t *testing.T) {
	builder := transcriptSession{store: nil, systemPrompt: "sys", resolver: nil, projectInstructions: nil}.build
	ctx := tool.WithSystemAddendum(context.Background(), "<project_context>\nproject_root: /repo/demo\n</project_context>")

	msgs, _, err := builder(ctx, []llm.ContentPart{llm.Text("pwd")})
	require.NoError(t, err)
	require.Len(t, msgs, 3)
	require.Equal(t, llm.RoleSystem, msgs[0].Role)
	require.Equal(t, "sys", msgs[0].TextContent())
	require.Equal(t, "pwd", msgs[1].TextContent())
	require.True(t, msgs[2].IsMeta)
	require.Contains(t, msgs[2].TextContent(), "project_root: /repo/demo")
}

func TestBuildTranscriptSessionInjectsExplicitSkillBeforeUser(t *testing.T) {
	builder := transcriptSession{store: nil, systemPrompt: "sys", resolver: nil, projectInstructions: nil}.build
	ctx := WithExplicitSkillActivation(context.Background(), "review", "<skill>\n<name>review</name>\nReview carefully.\n</skill>")

	msgs, _, err := builder(ctx, []llm.ContentPart{llm.Text("check this diff")})
	require.NoError(t, err)
	require.Len(t, msgs, 3)
	require.Equal(t, llm.RoleSystem, msgs[0].Role)
	require.True(t, msgs[1].IsMeta)
	require.Contains(t, msgs[1].TextContent(), "selected the `review` skill")
	require.Contains(t, msgs[1].TextContent(), "Review carefully.")
	require.Equal(t, llm.RoleUser, msgs[2].Role)
	require.False(t, msgs[2].IsMeta)
	require.Equal(t, "check this diff", msgs[2].TextContent())
}

// TestExplicitSkillActivationSurvivesFailedRetryWithoutDuplication is a
// regression test for a bug where an explicit-skill slash command (e.g.
// `/context-restore`) that failed on its first attempt (transient provider
// error) and was retried by the user produced a jumbled, duplicated
// transcript: the activation block, the user's turn, and the environment
// context were interleaved out of order with an extra copy of the user turn.
// The root cause was persisting the per-request activation message (which is
// rebuilt from context on every call) to the transcript, which misaligned
// AppendMessageSequence's prefix comparison against the pre-pipeline user row
// and caused the whole turn to be re-appended on the next attempt.
func TestExplicitSkillActivationSurvivesFailedRetryWithoutDuplication(t *testing.T) {
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sess := state.NewSessionStore(db, "main")

	ctx := llm.WithAgentSessionID(context.Background(), "s1")
	ctx = tool.WithSystemAddendum(ctx, "<project_context>\nproject_root: /repo\n</project_context>")
	ctx = WithExplicitSkillActivation(ctx, "context-restore", "<skill_instructions>restore</skill_instructions>")
	require.NoError(t, sess.Ensure(ctx, "s1", "s1"))

	turnText := "I ran /context-restore with no additional arguments. Carry out the activated skill's instructions for this session now."
	parts := []llm.ContentPart{llm.Text(turnText)}

	// Turn 1: dispatchUserTurnContent's pre-pipeline write persists the raw
	// user turn before the LLM call is made.
	_, err = sess.AppendStructuredMessage(ctx, "s1", "user", "/context-restore", "", state.MessagePartsJSON(llm.UserMessage(parts...), turnText), "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)

	builder := transcriptSession{store: sess, systemPrompt: "sys", resolver: nil, projectInstructions: nil}.build
	firstAttempt, _, err := builder(ctx, parts)
	require.NoError(t, err)

	// Turn 1's LLM call fails (e.g. a transient 503). The failure path
	// persists the partial capture, which is the constructed state.
	require.NoError(t, sess.AppendMessageSequence(ctx, "s1", firstAttempt, "", ""))

	// Turn 2: the user retries the same slash command. Another pre-pipeline
	// write persists another raw user turn.
	_, err = sess.AppendStructuredMessage(ctx, "s1", "user", "/context-restore", "", state.MessagePartsJSON(llm.UserMessage(parts...), turnText), "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)

	secondAttempt, _, err := builder(ctx, parts)
	require.NoError(t, err)

	// Exactly one ephemeral activation block, injected fresh for this call and
	// never duplicated from a persisted copy.
	activationCount := 0
	userCount := 0
	for _, m := range secondAttempt {
		text := m.TextContent()
		if strings.Contains(text, "skill for this request") {
			activationCount++
			require.True(t, m.Ephemeral, "activation block must be ephemeral")
		}
		if strings.Contains(text, "ran /context-restore") {
			userCount++
		}
	}
	require.Equal(t, 1, activationCount, "activation block must not be duplicated")
	// Two real user sends occurred (turn 1 failed, turn 2 retried), so two
	// occurrences is correct history - not the three the original bug produced
	// for these same two sends.
	require.Equal(t, 2, userCount, "two real sends must yield exactly two user turns, not three")
}

func TestBuildTranscriptSessionOnlyAppendsChangedEnvironment(t *testing.T) {
	addendum := "<project_context>\nproject_root: /repo/demo\n</project_context>"
	existing := promptCacheEnvironmentMessage(addendum, false)
	if _, changed := changedPromptCacheEnvironment([]llm.Message{existing}, addendum); changed {
		t.Fatal("unchanged environment must not be resent")
	}
	updated, changed := changedPromptCacheEnvironment([]llm.Message{existing}, addendum+"\ncurrent_working_directory: /repo/demo/sub")
	require.True(t, changed)
	require.True(t, updated.IsMeta)
	require.Contains(t, updated.TextContent(), "current_working_directory")
}

func TestBuildTranscriptSessionKeepsPriorRequestAsExactPrefix(t *testing.T) {
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sess := state.NewSessionStore(db, "main")
	ctx := llm.WithAgentSessionID(context.Background(), "cache-prefix")
	ctx = tool.WithSystemAddendum(ctx, "<project_context>stable</project_context>")
	require.NoError(t, sess.Ensure(ctx, "cache-prefix", "cache-prefix"))

	firstUser := llm.UserMessage(llm.Text("first"))
	require.NoError(t, sess.AppendMessageSequence(ctx, "cache-prefix", []llm.Message{firstUser}, "", ""))
	builder := transcriptSession{store: sess, systemPrompt: "stable system", resolver: nil, projectInstructions: nil}.build
	first, _, err := builder(ctx, firstUser.Parts)
	require.NoError(t, err)
	require.Len(t, first, 3)

	firstCompleted := append(append([]llm.Message(nil), first...), llm.AssistantMessage([]llm.ContentPart{llm.Text("done")}))
	require.NoError(t, sess.AppendMessageSequence(ctx, "cache-prefix", firstCompleted, "", ""))
	secondUser := llm.UserMessage(llm.Text("second"))
	require.NoError(t, sess.AppendMessageSequence(ctx, "cache-prefix", []llm.Message{
		firstUser,
		first[2],
		firstCompleted[3],
		secondUser,
	}, "", ""))

	second, _, err := builder(ctx, secondUser.Parts)
	require.NoError(t, err)
	require.Greater(t, len(second), len(first))
	require.Equal(t, first, second[:len(first)], "the previous request must remain an exact prefix")
	require.Equal(t, 1, countPromptCacheEnvironmentMessages(second), "unchanged environment must not be resent")
}

func countPromptCacheEnvironmentMessages(messages []llm.Message) int {
	count := 0
	for _, msg := range messages {
		if msg.IsMeta && strings.HasPrefix(msg.TextContent(), promptCacheEnvironmentPrefix) {
			count++
		}
	}
	return count
}

// TestBuildTranscriptSessionReplacesRawImageUserWithEnriched is a regression
// test for a duplicate-user-message bug on image-attachment turns.
//
// dispatchUserTurnContent persists a raw user message (text + file_reference)
// before the run. The pipeline then hands the runner enriched parts containing
// the text and base64 image. The builder loads the transcript (which already
// contains the raw user) and must replace it with the enriched version, not
// append a second user message.
func TestBuildTranscriptSessionReplacesRawImageUserWithEnriched(t *testing.T) {
	db, err := state.OpenStateForTest(context.Background(), filepath.Join(t.TempDir(), "state.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	sess := state.NewSessionStore(db, "main")
	require.NotNil(t, sess)
	ctx := llm.WithAgentSessionID(context.Background(), "s1")
	require.NoError(t, sess.Ensure(ctx, "s1", "s1"))

	// Real tiny PNG so rehydrate can read + encode it from the file_reference.
	img := filepath.Join(t.TempDir(), "clip.png")
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(img, png, 0o600))

	userText := "explain this image"
	// Persist the raw user turn exactly as buildSurfaceUserPartsJSON does for
	// images: text + file_reference (no "[attachment parsed]" marker).
	rawParts := state.MessagePartsJSON(llm.UserMessage(llm.Text(userText)), userText)
	rawParts = state.AppendRawPartJSON(rawParts, state.FileReferencePartJSON(img, "clip.png", "image/png"))
	_, err = sess.AppendStructuredMessage(ctx, "s1", "user", userText, "", rawParts, "", "", "", "", state.MessageExecTiming{})
	require.NoError(t, err)

	// Enriched parts the pipeline hands to the runner keep the user text intact.
	imgPart, err := llm.ImageFile(img)
	require.NoError(t, err)
	enriched := []llm.ContentPart{llm.Text(userText), imgPart}

	builder := transcriptSession{store: sess, systemPrompt: "sys", resolver: PathResolver{}, projectInstructions: nil}.build
	msgs, _, err := builder(ctx, enriched)
	require.NoError(t, err)

	// system + single user (enriched, replacing the raw one) = 2 messages.
	require.Len(t, msgs, 2)
	require.Equal(t, llm.RoleSystem, msgs[0].Role)
	require.Equal(t, llm.RoleUser, msgs[1].Role)
	require.Equal(t, userText, msgs[1].TextContent())

	userCount := 0
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			userCount++
		}
	}
	require.Equal(t, 1, userCount, "duplicate user message sent to LLM")
}

// A process serves both surfaces from one Runner, and they record file
// references in different namespaces: the web surface an upload ID, a terminal
// attachment an absolute path. Resolving only one namespace let every replayed
// terminal image fall back to its text placeholder, so the model never saw the
// picture the user had attached.
func TestChainResolverCoversBothFileIDNamespaces(t *testing.T) {
	uploads := FilertResolver{Resolve: func(_ context.Context, fileID string) (string, bool) {
		if fileID == "upload-1" {
			return "/srv/uploads/upload-1.png", true
		}
		return "", false
	}}
	resolver := ChainResolver{uploads, PathResolver{}}

	if got, ok := resolver.ResolveFilePath(context.Background(), "upload-1"); !ok || got != "/srv/uploads/upload-1.png" {
		t.Fatalf("upload id resolved to %q ok=%v", got, ok)
	}
	if got, ok := resolver.ResolveFilePath(context.Background(), "/home/ada/shot.png"); !ok || got != "/home/ada/shot.png" {
		t.Fatalf("attachment path resolved to %q ok=%v", got, ok)
	}
	if _, ok := resolver.ResolveFilePath(context.Background(), "not-an-id-or-path"); ok {
		t.Fatal("an id in neither namespace must not resolve")
	}
	if _, ok := (ChainResolver{nil}).ResolveFilePath(context.Background(), "/abs"); ok {
		t.Fatal("a nil member must be skipped, not resolve")
	}
}

// The first resolver that answers wins, so an upload ID is never re-read as a
// path and a path is never looked up as an upload.
func TestChainResolverTakesTheFirstResolverThatAnswers(t *testing.T) {
	first := FilertResolver{Resolve: func(context.Context, string) (string, bool) { return "/first", true }}
	second := FilertResolver{Resolve: func(context.Context, string) (string, bool) { return "/second", true }}
	if got, _ := (ChainResolver{first, second}).ResolveFilePath(context.Background(), "x"); got != "/first" {
		t.Fatalf("chain resolved to %q, want /first", got)
	}
}
