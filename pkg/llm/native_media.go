package llm

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// MediaKind names one kind of non-image media a model can be handed directly:
// a PDF document, an audio clip, a video. The wire shapes each protocol has for
// them are recorded in docs/plan/NATIVE_MEDIA_INPUT_FACTS.md.
type MediaKind string

const (
	MediaKindPDF   MediaKind = "pdf"
	MediaKindAudio MediaKind = "audio"
	MediaKindVideo MediaKind = "video"
)

// NativeMediaInlineLimit is the largest file this build sends inline as base64.
//
// The protocols publish different ceilings, and several could not be verified in
// the environment this landed in (see docs/plan/NATIVE_MEDIA_INPUT_FACTS.md: the
// smallest documented one that could be was 25 MB, 火山方舟's base64 audio). One
// conservative limit below that holds every kind, so a file accepted for one
// model stays accepted by the next model the conversation switches to. A larger
// file degrades to text exactly as an unsupported kind does, instead of being
// rejected by the provider after the whole conversation has been re-sent.
const NativeMediaInlineLimit = 20 << 20

// ErrNativeMediaTooLarge reports that a file's bytes are larger than
// NativeMediaInlineLimit: the attachment it came from degrades to text instead
// of being sent.
var ErrNativeMediaTooLarge = errors.New("file is larger than the largest one sent inline")

// NativeMediaKinds is the set of non-image media kinds something can carry:
// what a model accepts, or what a protocol has a shape for.
type NativeMediaKinds struct {
	PDF   bool
	Audio bool
	Video bool
}

// Has reports whether kind is in the set.
func (k NativeMediaKinds) Has(kind MediaKind) bool {
	switch kind {
	case MediaKindPDF:
		return k.PDF
	case MediaKindAudio:
		return k.Audio
	case MediaKindVideo:
		return k.Video
	}
	return false
}

// Every is the set of kinds both sides carry: a model's own capability narrowed
// by what the protocol it is spoken to can express.
func (k NativeMediaKinds) Every(other NativeMediaKinds) NativeMediaKinds {
	return NativeMediaKinds{
		PDF:   k.PDF && other.PDF,
		Audio: k.Audio && other.Audio,
		Video: k.Video && other.Video,
	}
}

// Any reports whether the set carries at least one kind.
func (k NativeMediaKinds) Any() bool { return k.PDF || k.Audio || k.Video }

// NativeMediaKindOfMIME names the kind of a MIME type, "" for anything this
// build does not send natively (an image has its own kinds; text is text).
func NativeMediaKindOfMIME(mimeType string) MediaKind {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if i := strings.IndexByte(mimeType, ';'); i >= 0 {
		mimeType = strings.TrimSpace(mimeType[:i])
	}
	switch {
	case mimeType == "application/pdf":
		return MediaKindPDF
	case strings.HasPrefix(mimeType, "audio/"):
		return MediaKindAudio
	case strings.HasPrefix(mimeType, "video/"):
		return MediaKindVideo
	}
	return ""
}

// NativeMediaMIMEType names the MIME type of file bytes by sniffing the first
// 512 bytes, or "" when they are not a PDF, audio or video this build sends.
//
// Like ImageMIMEType, the sniff is authoritative and the extension is never
// consulted: an extension can only relabel bytes, and a file sent under a type
// its bytes do not have is rejected as a whole request. path is not read; it
// stays in the signature for the callers that have one.
func NativeMediaMIMEType(path string, data []byte) string {
	head := data
	if len(head) > 512 {
		head = head[:512]
	}
	sniffed := strings.ToLower(http.DetectContentType(head))
	if NativeMediaKindOfMIME(sniffed) == "" {
		return ""
	}
	return sniffed
}

// NativeMediaMIMETypeOfFile names the MIME type of the file at path by reading
// its leading bytes, "" when it cannot be read or is not native media. It is
// what a replay asks about a file_reference stored before its type was: the row
// records the type an attachment arrived with, and a reference stored without
// one still has its bytes.
func NativeMediaMIMETypeOfFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := f.Read(head)
	return NativeMediaMIMEType(path, head[:n])
}

// Interface protocols this build sends a request on. A protocol, not a
// provider: two providers may speak the same one, and one provider's models may
// speak different ones.
const (
	ProtocolChatCompletions = "chat_completions"
	ProtocolResponses       = "responses"
	ProtocolMessages        = "messages"
	ProtocolGemini          = "gemini"
)

// NativeMediaForProtocol is what a protocol has a documented shape for
// (docs/plan/NATIVE_MEDIA_INPUT_FACTS.md): Chat Completions carries all three,
// the Responses API and the Anthropic Messages API carry a PDF, and Gemini's
// inline_data is documented for audio and video — its document input could not
// be verified from an official source here, so a PDF stays text on it.
func NativeMediaForProtocol(protocol string) NativeMediaKinds {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case ProtocolResponses, ProtocolMessages:
		return NativeMediaKinds{PDF: true}
	case ProtocolGemini:
		return NativeMediaKinds{Audio: true, Video: true}
	case ProtocolChatCompletions:
		return NativeMediaKinds{PDF: true, Audio: true, Video: true}
	}
	return NativeMediaKinds{}
}

// ProtocolForProvider names the protocol a provider's requests are sent on.
//
// It is the same routing NewLLMFromYAML makes, restated where a reader that has
// no client can ask: a provider whose endpoint speaks a protocol under its own
// name reaches the same client. OpenCode's models pick their protocol per model
// (the run layer asks the gateway's table), so that decision is not here.
func ProtocolForProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic", "minimax":
		return ProtocolMessages
	case "chatgpt":
		return ProtocolResponses
	case "google":
		return ProtocolGemini
	}
	return ProtocolChatCompletions
}

// nativeMediaByProvider is what a provider's own endpoint can be handed
// directly, from the official documents recorded in
// docs/plan/NATIVE_MEDIA_INPUT_FACTS.md. A provider absent from this table
// carries nothing: an unpublished shape is not a shape, and sending a file a
// vendor never documented is what today's text degradation exists to avoid. A
// configured row's own switches (ModelOverride) can still turn a capability on
// over the catalog; this table is the protocol's half of the answer.
var nativeMediaByProvider = map[string]NativeMediaKinds{
	// OpenAI: `file` with a base64 data URL for a PDF, `input_audio` for audio.
	// Video has no content part at all.
	"openai": {PDF: true, Audio: true},
	// The Codex/ChatGPT backend speaks the Responses API: `input_file` only.
	"chatgpt": {PDF: true},
	// Anthropic Messages: a `document` block with a base64 PDF source.
	"anthropic": {PDF: true},
	// Gemini's inline_data is documented for audio and video; its PDF input has
	// no official shape this build could verify, so a PDF stays text there.
	"google": {Audio: true, Video: true},
	// 火山方舟 (Doubao): the Chat API documents base64 for audio (input_audio)
	// and video (video_url); its PDF is documented only as a Files API upload,
	// so a PDF stays text until a file upload is wired in.
	"volcengine": {Audio: true, Video: true},
	"doubao":     {Audio: true, Video: true},
	"byteplus":   {Audio: true, Video: true},
}

// NativeMediaForProvider is what a provider's endpoint carries natively, a zero
// set for a provider this build has no official shape for.
func NativeMediaForProvider(provider string) NativeMediaKinds {
	return nativeMediaByProvider[strings.ToLower(strings.TrimSpace(provider))]
}

// NativeMediaTooLargeNote is the model-visible line a file part degrades to
// when its bytes are larger than NativeMediaInlineLimit: the attachment's label
// and type, and the reason, in the one sentence every degraded attachment
// already uses.
func NativeMediaTooLargeNote(label, mimeType string) string {
	return UnsendableImageNote(label, mimeType,
		"file is larger than the largest one sent inline, so the model cannot be handed its bytes")
}

// filePartKey names a file part in a diagnostic: its type and rough size, never
// its bytes. Token accounting and telemetry record that a file was sent, not
// what was in it.
func filePartKey(part ContentPart) (mimeType string, bytes int) {
	mimeType = strings.TrimSpace(part.MIMEType)
	if mimeType == "" {
		mimeType = "unknown type"
	}
	// 4 base64 characters carry 3 bytes.
	return mimeType, len(part.FileBase64) / 4 * 3
}

// FilePartTokens is what one file part adds to an occupancy estimate: its
// decoded size over four, which is what the same bytes would cost as text. A
// part without bytes costs nothing.
func FilePartTokens(part ContentPart) int {
	_, size := filePartKey(part)
	return size / 4
}

// FilePartSummary is one file part as a log, a row or an estimator sees it:
// what it was, how big, and the name it was labelled with — never the bytes.
// Every serializer that describes a file part describes it through here, so the
// same part is never summarized two ways.
func FilePartSummary(part ContentPart) map[string]any {
	mimeType, size := filePartKey(part)
	out := map[string]any{"type": "file", "mime_type": mimeType, "bytes": size}
	if name := strings.TrimSpace(part.FileName); name != "" {
		out["file_name"] = filepath.Base(name)
	}
	return out
}
