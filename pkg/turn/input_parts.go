package turn

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/state"
)

// SurfaceTurnContentParts builds the model-facing content parts for a turn
// with attachments: the text part plus one image part per attachment. It
// also returns the display form of the text, with an "[Image #N]" marker
// appended per image actually sent — this is not display-only (see
// DisplayTextOrUserText's doc comment): the caller uses the returned
// display text to compute the actual model input too, so the marker
// reaches both the persisted transcript and the model's own context.
//
// An attachment whose bytes cannot be sent does not fail the turn. It
// degrades to an llm.UnsendableImageNote text part — the same sentence the
// replay of this row writes for that file_reference — and is reported in the
// returned warnings so the user learns which picture is missing and why. The
// turn continues: the model can still do the task by another route (ask for
// a converted file, read it as text, work from a description).
//
// It does not apply the image-input gate: a message refused for a text-only
// session model is still kept in the conversation, so the caller needs its
// display text and stored parts either way and refuses only the request.
func SurfaceTurnContentParts(userText string, displayText string, attachments []InputAttachment) ([]llm.ContentPart, string, []string, error) {
	text := strings.TrimSpace(userText)
	if text == "" {
		text = "Please analyze the attached image."
	}
	parts := []llm.ContentPart{llm.Text(text)}
	display := strings.TrimSpace(displayText)
	if display == "" {
		display = strings.TrimSpace(userText)
	}
	var warnings []string
	sent := 0
	for _, att := range attachments {
		path := strings.TrimSpace(att.Path)
		if path == "" {
			continue
		}
		part, err := llm.ImageFile(path)
		if err != nil {
			// Self-heal instead of failing the turn: the model is told which
			// image could not be sent and why — the same sentence the replay of
			// this row writes — and the rest of the message still runs. The
			// file_reference stays in parts_json, so a later replay retries the
			// image through the same ladder.
			label, mimeType := attachmentLabelAndMIME(att)
			note := llm.UnsendableImageNote(label, mimeType, err.Error())
			parts = append(parts, llm.Text(note))
			warnings = append(warnings, note)
			continue
		}
		sent++
		parts = append(parts, part)
		display = strings.TrimSpace(display + "\n[Image #" + fmt.Sprintf("%d", sent) + "]")
	}
	if len(parts) == 1 {
		return nil, "", nil, fmt.Errorf("no valid image attachments")
	}
	return parts, display, warnings, nil
}

// attachmentLabelAndMIME names an attachment exactly the way it is stored: the
// label it was given (else the base name of its path, else the path), and the
// type it was given (else the one its bytes name). Both the live parts above
// and the persisted parts_json below go through here, because the note a live
// turn writes replaces the placeholder its own reference replays as — a note
// that named the file differently from its reference would fork the cached
// prefix at that message.
func attachmentLabelAndMIME(att InputAttachment) (label, mimeType string) {
	path := strings.TrimSpace(att.Path)
	label = strings.TrimSpace(att.Label)
	if label == "" {
		label = filepath.Base(path)
	}
	if label == "." || label == string(filepath.Separator) || label == "" {
		label = path
	}
	mimeType = strings.TrimSpace(att.MIMEType)
	if mimeType == "" {
		// An image picked with @ arrives as a bare path. Replay shows the
		// model a file_reference as the image only when it is typed as one,
		// so the type its bytes name is recorded with it.
		mimeType = sniffImageMIMEType(path)
	}
	return label, mimeType
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
		label, mimeType := attachmentLabelAndMIME(att)
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
		// surface path only accepts attachments it can build an image part for
		// (one it cannot is degraded to text by SurfaceTurnContentParts), so
		// every attachment reaching here is an image or a file whose replay
		// reports the same reason the live turn did.
	}
	return partsJSON
}

// sniffImageMIMEType names the image type of the file at path from its leading
// bytes, or "" when it cannot be read or is not an image a provider accepts.
func sniffImageMIMEType(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	return llm.ImageMIMEType(path, head[:n])
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
