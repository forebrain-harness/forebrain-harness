// Transcript assembly: session builder, image rehydration, and previews.
package run

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
	"github.com/forebrain-harness/forebrain-harness/pkg/tool"
)

const promptCacheEnvironmentPrefix = "<forebrain_environment_context>\n"

// projectInstructionsMessage wraps one project's frozen instructions as a
// meta user message placed immediately after the system prompt.
func projectInstructionsMessage(body string) llm.Message {
	msg := llm.UserMessage(llm.Text(projectInstructionsPrefix + "\n" + strings.TrimSpace(body) + "\n</forebrain_project_instructions>"))
	msg.IsMeta = true
	return msg
}

// projectInstructionsPrefix marks the message so tooling can recognize it and
// so it can never be confused with a user turn.
const projectInstructionsPrefix = "<forebrain_project_instructions>"

func promptCacheEnvironmentMessage(addendum string, cleared bool) llm.Message {
	body := strings.TrimSpace(addendum)
	if body == "" && cleared {
		body = "(cleared)"
	}
	msg := llm.UserMessage(llm.Text(promptCacheEnvironmentPrefix + body + "\n</forebrain_environment_context>"))
	msg.IsMeta = true
	return msg
}

func isPromptCacheEnvironmentMessage(message llm.Message) bool {
	return message.IsMeta && strings.HasPrefix(message.TextContent(), promptCacheEnvironmentPrefix)
}

func latestPromptCacheEnvironmentMessage(messages []llm.Message) (llm.Message, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		if isPromptCacheEnvironmentMessage(messages[i]) {
			return messages[i], true
		}
	}
	return llm.Message{}, false
}

func latestPromptCacheEnvironment(messages []llm.Message) (string, bool) {
	message, found := latestPromptCacheEnvironmentMessage(messages)
	if !found {
		return "", false
	}
	return message.TextContent(), true
}

func changedPromptCacheEnvironment(messages []llm.Message, addendum string) (llm.Message, bool) {
	current, found := latestPromptCacheEnvironment(messages)
	if strings.TrimSpace(addendum) == "" {
		cleared := promptCacheEnvironmentMessage("", true)
		return cleared, found && current != cleared.TextContent()
	}
	desired := promptCacheEnvironmentMessage(addendum, false)
	return desired, !found || current != desired.TextContent()
}

// transcriptSession assembles what a request of the conversation carries. A
// turn is its head, its stored history and the turn's input; a compaction's
// summary request is the same head and history with the summary instruction
// in place of the input, which is what lets it reuse the conversation's
// cached prompt prefix. Both are built here so the two can never disagree
// about what precedes the input.
type transcriptSession struct {
	store               *state.SessionStore
	systemPrompt        string
	resolver            FileReferenceResolver
	projectInstructions func(sessionID string) string
}

// head is the byte-stable start of every request: the system prompt and the
// session's project instructions. Project instructions are the one piece of
// dynamic context allowed this early: they ride as a meta message directly
// after the system prompt, so the tools and system bytes ahead of them never
// move, and the composition root freezes the string for the session's
// lifetime — an edit mid-session lands in the next session.
func (t transcriptSession) head(ctx context.Context) []llm.Message {
	// Keep the instruction prefix byte-stable. Dynamic project/rules
	// context is appended as a meta input, instead of rewriting the system prompt.
	messages := []llm.Message{llm.SystemMessage(t.systemPrompt)}
	if t.projectInstructions != nil {
		sessionID := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
		if body := strings.TrimSpace(t.projectInstructions(sessionID)); body != "" {
			messages = append(messages, projectInstructionsMessage(body))
		}
	}
	return messages
}

// history is the stored conversation as the next request carries it.
//
// The persisted transcript is healed before it is read. A resume (approval
// flow) reaches this without going through dispatchUserTurnContent, so without
// this the store's orphan/duplicate tool-result rows left by a divergent resume
// snapshot would be sent verbatim and rejected with "Messages with role 'tool'
// must be a response to a preceding message with 'tool_calls'". Best-effort: a
// repair error must not abort the turn.
//
// During a tool-approval resume turn, the repair is skipped: the dangling
// tool_calls row left by persistRequiresActionSnapshot must survive so the
// resume caller can fill it with the replayed tool result after the run
// completes. The loaded transcript is overridden by the ToolApprovalResumeState
// snapshot inside the orchestration LLM, so any transient dangling rows never
// reach the LLM during the resume itself.
func (t transcriptSession) history(ctx context.Context, sessionID string) ([]llm.Message, error) {
	if tool.ToolApprovalResumeFromContext(ctx) == nil {
		_, _ = t.store.RepairDanglingToolResults(ctx, sessionID)
	}
	entries, err := t.store.ListTranscriptMessagesWithRefs(ctx, sessionID, 5000)
	if err != nil {
		return nil, err
	}
	transcript := rehydrateImageReferences(ctx, t.resolver, entries)
	if len(transcript) == 0 {
		transcript = []llm.Message{}
	}
	return transcript, nil
}

// build is the agent's session builder: the head, the history and the turn's
// input.
func (t transcriptSession) build(ctx context.Context, parts []llm.ContentPart) ([]llm.Message, int, error) {
	messages := t.head(ctx)
	addendum := strings.TrimSpace(tool.SystemAddendumFromContext(ctx))
	explicitActivation, hasExplicitActivation := explicitSkillActivationFromContext(ctx)
	if t.store == nil {
		if hasExplicitActivation && len(parts) > 0 {
			messages = append(messages, explicitSkillActivationMessage(explicitActivation))
		}
		if len(parts) > 0 {
			messages = append(messages, llm.UserMessage(parts...))
		}
		if addendum != "" {
			messages = append(messages, promptCacheEnvironmentMessage(addendum, false))
		}
		return messages, len(messages) - maxOne(len(parts)), nil
	}
	sessionID := strings.TrimSpace(llm.AgentSessionIDFromContext(ctx))
	if sessionID == "" {
		if hasExplicitActivation && len(parts) > 0 {
			messages = append(messages, explicitSkillActivationMessage(explicitActivation))
		}
		if len(parts) > 0 {
			messages = append(messages, llm.UserMessage(parts...))
		}
		return messages, len(messages) - maxOne(len(parts)), nil
	}
	transcript, err := t.history(ctx, sessionID)
	if err != nil {
		return nil, 0, err
	}
	messages = append(messages, transcript...)
	if len(parts) == 0 {
		if environment, changed := changedPromptCacheEnvironment(transcript, addendum); changed {
			messages = append(messages, environment)
		}
		return messages, len(messages), nil
	}
	currentUser := llm.UserMessage(parts...)
	currentUser.IsMeta = goalContinuationInputFromContext(ctx)
	if len(transcript) > 0 && transcript[len(transcript)-1].Role == llm.RoleUser {
		// The pre-pipeline write in dispatchUserTurnContent already stored a
		// plain user message. Replace it with the pipeline-enriched version
		// rather than appending a duplicate.
		//
		// Pipeline annotations can change the literal content, so check
		// whether the enriched text contains the stored raw text. When it
		// does, they represent the same user turn and the enriched version
		// replaces the raw one.
		lastText := llm.TextContent(transcript[len(transcript)-1].Parts...)
		curText := llm.TextContent(currentUser.Parts...)
		if lastText != "" && strings.Contains(curText, lastText) {
			messages = messages[:len(messages)-1]
			if hasExplicitActivation {
				messages = append(messages, explicitSkillActivationMessage(explicitActivation))
			}
			messages = append(messages, currentUser)
		} else {
			if hasExplicitActivation {
				messages = append(messages, explicitSkillActivationMessage(explicitActivation))
			}
			messages = append(messages, currentUser)
		}
	} else {
		if hasExplicitActivation {
			messages = append(messages, explicitSkillActivationMessage(explicitActivation))
		}
		messages = append(messages, currentUser)
	}
	if environment, changed := changedPromptCacheEnvironment(transcript, addendum); changed {
		messages = append(messages, environment)
	}
	return messages, len(messages), nil
}

func maxOne(n int) int {
	if n > 0 {
		return 1
	}
	return 0
}

// FileReferenceResolver maps a file_id (from a persisted file_reference part)
// to an absolute filesystem path. The file_id may be:
//   - An absolute path already (TUI attachments)
//   - An upload ID that needs resolution via a file service (gateway uploads)
//
// Implementations are expected to be safe for concurrent use.
type FileReferenceResolver interface {
	ResolveFilePath(ctx context.Context, fileID string) (absPath string, ok bool)
}

// PathResolver treats file_id as an absolute filesystem path and returns it
// unchanged. It resolves what BuildSurfaceUserPartsJSON writes: a surface
// attachment is persisted by path rather than by copying its bytes into the
// transcript, so the recorded file_id is the path itself. Existence is not
// checked here — the caller reads the file next and falls back to the text
// placeholder when that fails.
type PathResolver struct{}

func (PathResolver) ResolveFilePath(_ context.Context, fileID string) (string, bool) {
	abs := strings.TrimSpace(fileID)
	if abs == "" || !filepath.IsAbs(abs) {
		return "", false
	}
	return abs, true
}

// ChainResolver tries each resolver in order and takes the first that
// resolves. It exists because the two surfaces write file_ids in two different
// namespaces — the gateway records an upload ID, a terminal attachment records
// an absolute path — while a process serves both from one Runner. Installing a
// single namespace's resolver silently drops the other surface's images: the
// reference resolves to nothing and rehydration leaves the text placeholder
// the model cannot see.
type ChainResolver []FileReferenceResolver

func (c ChainResolver) ResolveFilePath(ctx context.Context, fileID string) (string, bool) {
	for _, r := range c {
		if r == nil {
			continue
		}
		if abs, ok := r.ResolveFilePath(ctx, fileID); ok {
			return abs, true
		}
	}
	return "", false
}

// FilertResolver wraps a file-service resolution function that maps a file_id
// (upload ID) to an absolute filesystem path. It is used by the gateway to
// resolve uploaded file references.
type FilertResolver struct {
	Resolve func(ctx context.Context, fileID string) (absPath string, ok bool)
}

func (r FilertResolver) ResolveFilePath(ctx context.Context, fileID string) (string, bool) {
	if r.Resolve == nil {
		return "", false
	}
	return r.Resolve(ctx, fileID)
}

// rehydrateImageReferences scans transcript entries and replaces image-typed
// file_reference text placeholders with real ImageBase64 parts. The file_id
// from each reference is resolved via the given resolver and read+encoded
// via llm.ImageFile.
//
// Rehydration is best-effort: any failure (missing resolver, unresolved path,
// unreadable file, unsupported format) leaves the original text placeholder
// intact. The function never returns an error.
func rehydrateImageReferences(ctx context.Context, resolver FileReferenceResolver, entries []state.TranscriptEntry) []llm.Message {
	out := make([]llm.Message, len(entries))
	for i, e := range entries {
		out[i] = rehydrateMessageImages(ctx, resolver, e.Message, e.FileRefs)
	}
	return out
}

// UserInputParts is what the model is sent for a user message stored as
// partsJSON: parsed, and its images rehydrated, exactly as the conversation's
// history is. A surface that builds a turn's input from the row it stores for
// it sends the same message now as every later request replays, so the
// request's prefix stays cached and the turn's own message is never written
// to the transcript a second time.
func UserInputParts(ctx context.Context, resolver FileReferenceResolver, partsJSON string) []llm.ContentPart {
	parts, _, _, _ := state.ParseMessageParts(partsJSON, "")
	return rehydrateMessageImages(ctx, resolver, llm.UserMessage(parts...), state.ExtractFileRefInfos(partsJSON)).Parts
}

// rehydrateMessageImages replaces the text placeholder ParseMessageParts
// leaves for each image-typed file_reference of a user message with the image
// itself, read through resolver.
func rehydrateMessageImages(ctx context.Context, resolver FileReferenceResolver, msg llm.Message, refs []state.FileRefInfo) llm.Message {
	if resolver == nil || len(refs) == 0 || msg.Role != llm.RoleUser {
		return msg
	}
	// Clone the parts slice so we can replace in place.
	parts := make([]llm.ContentPart, len(msg.Parts))
	copy(parts, msg.Parts)
	for _, ref := range refs {
		mime := strings.TrimSpace(ref.MIMEType)
		if !strings.HasPrefix(mime, "image/") {
			continue
		}
		absPath, ok := resolver.ResolveFilePath(ctx, ref.FileID)
		if !ok {
			slog.Debug("rehydrate: cannot resolve file_id", "file_id", ref.FileID, "mime", mime)
			continue
		}
		imgPart, err := llm.ImageFile(absPath)
		if err != nil {
			slog.Debug("rehydrate: image file read failed", "path", absPath, "err", err)
			continue
		}
		// Find and replace the text placeholder that ParseMessageParts
		// would have produced for this file_reference.
		placeholder := fileReferencePlaceholder(ref)
		replaced := false
		for j := range parts {
			if parts[j].Type == llm.ContentTypeText && strings.TrimSpace(parts[j].Text) == placeholder {
				parts[j] = imgPart
				replaced = true
				break
			}
		}
		if !replaced {
			slog.Debug("rehydrate: placeholder not found in message parts", "placeholder", placeholder)
		}
	}
	msg.Parts = parts
	return msg
}

// fileReferencePlaceholder reconstructs the text that ParseMessageParts
// produces for a file_reference part. This must match exactly the format used
// in message_parts.go:234-247.
func fileReferencePlaceholder(ref state.FileRefInfo) string {
	text := strings.TrimSpace(ref.Label)
	if text == "" {
		text = ref.FileID
	}
	if mime := strings.TrimSpace(ref.MIMEType); mime != "" {
		text = strings.TrimSpace(text + " (" + mime + ")")
	}
	if text != "" {
		text = "[attachment] " + text
	}
	return text
}

func InputPreview(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:max]
}
