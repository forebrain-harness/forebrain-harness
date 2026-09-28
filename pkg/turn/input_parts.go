package turn

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// SurfaceTurnContentParts builds the model-facing content parts for a turn
// with attachments: the text part plus one image part per attachment. It
// also returns the display form of the text, with an "[Image #N]" marker
// appended per attachment — this is not display-only (see
// DisplayTextOrUserText's doc comment): the caller uses the returned
// display text to compute the actual model input too, so the marker
// reaches both the persisted transcript and the model's own context.
func SurfaceTurnContentParts(userText string, displayText string, attachments []InputAttachment) ([]llm.ContentPart, string, error) {
	text := strings.TrimSpace(userText)
	if text == "" {
		text = "Please analyze the attached image."
	}
	parts := []llm.ContentPart{llm.Text(text)}
	display := strings.TrimSpace(displayText)
	if display == "" {
		display = strings.TrimSpace(userText)
	}
	for i, att := range attachments {
		path := strings.TrimSpace(att.Path)
		if path == "" {
			continue
		}
		part, err := llm.ImageFile(path)
		if err != nil {
			return nil, "", fmt.Errorf("attach image %q: %w", path, err)
		}
		parts = append(parts, part)
		label := strings.TrimSpace(att.Label)
		if label == "" {
			label = filepath.Base(path)
		}
		if label == "." || label == string(filepath.Separator) || label == "" {
			label = path
		}
		display = strings.TrimSpace(display + "\n[Image #" + fmt.Sprintf("%d", i+1) + "]")
	}
	if len(parts) == 1 {
		return nil, "", fmt.Errorf("no valid image attachments")
	}
	return parts, display, nil
}

// BuildSurfaceUserPartsJSON builds the persisted, replay-form parts_json for
// a user turn with attachments: a text part plus one file_reference part per
// attachment. Attachment bytes are never inlined — replaying a session
// re-reads the file from its recorded path rather than duplicating the image
// data into the database.
func BuildSurfaceUserPartsJSON(userText string, attachments []InputAttachment) string {
	text := strings.TrimSpace(userText)
	partsJSON := state.MessagePartsJSON(llm.UserMessage(llm.Text(text)), text)
	for _, att := range attachments {
		path := strings.TrimSpace(att.Path)
		if path == "" {
			continue
		}
		label := strings.TrimSpace(att.Label)
		if label == "" {
			label = filepath.Base(path)
		}
		if label == "." || label == string(filepath.Separator) || label == "" {
			label = path
		}
		mimeType := strings.TrimSpace(att.MIMEType)
		fileRef := state.FileReferencePartJSON(path, label, mimeType)
		if strings.TrimSpace(fileRef) != "" {
			partsJSON = state.AppendRawPartJSON(partsJSON, fileRef)
		}
		// Skip the attachment_parse text marker for image attachments: images
		// are delivered to the model as base64 parts (rehydrated from the
		// file_reference above), so an "[attachment parsed]" text part is
		// redundant. It must not be persisted because it would be the only
		// textual difference between this raw user turn and the
		// guardrail-wrapped turn sent to the LLM, defeating the transcript
		// dedup and producing a duplicate user message in the request. The
		// surface path only accepts image attachments (SurfaceTurnContentParts
		// rejects anything llm.ImageFile cannot decode), so every attachment
		// reaching here is an image.
	}
	return partsJSON
}

// DisplayTextOrUserText resolves the model input for a turn: displayText
// when set, unless it is a TUI slash-command rendering hint (a leading "/")
// while the actual model input (userText) is not itself a slash command, in
// which case the raw command text must not override the real model input.
func DisplayTextOrUserText(userText string, displayText string) string {
	if strings.HasPrefix(strings.TrimSpace(displayText), "/") && !strings.HasPrefix(strings.TrimSpace(userText), "/") {
		return userText
	}
	if strings.TrimSpace(displayText) != "" {
		return displayText
	}
	return userText
}
