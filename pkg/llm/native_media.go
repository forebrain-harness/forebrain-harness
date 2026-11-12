package llm

import (
	"context"
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

// Without returns the set with every kind in refused removed: what a
// conversation's model still accepts after one kind was refused on it.
func (k NativeMediaKinds) Without(refused NativeMediaKinds) NativeMediaKinds {
	return NativeMediaKinds{
		PDF:   k.PDF && !refused.PDF,
		Audio: k.Audio && !refused.Audio,
		Video: k.Video && !refused.Video,
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

// IsMediaRejection reports whether err is the provider refusing one kind of
// native media, and the sentence to remember when it is.
//
// The judgement is deliberately narrower than IsImageRejection's, because what
// a caller does with a hit is switch that kind off for the rest of the
// conversation: a false positive costs a capability the user only gets back by
// switching model, while a false negative costs the request that already
// failed. So a hit needs both halves:
//
//   - the provider's own rejection. err must be an *APIError carrying 400 or
//     413 — the two statuses IsImageRejection reads as a rejection. A 5xx, a
//     429, a timeout, or any error that never reached the provider carries no
//     such status and never matches: the media is not what failed, and
//     degrading it would hide an outage behind a text-only request.
//   - text about this kind: it names the kind (pdf/document, audio/input_audio,
//     video/video_url), or it is one of the generic unsupported-content
//     sentences and names no other native kind — a provider saying it does not
//     support audio is refusing the audio, not the PDF travelling beside it.
func IsMediaRejection(err error, kind MediaKind) (reason string, ok bool) {
	if len(mediaKindWords(kind)) == 0 {
		return "", false
	}
	text, sentence, rejected := mediaRejectionText(err)
	if !rejected {
		return "", false
	}
	lowered := strings.ToLower(text)
	for _, word := range mediaKindWords(kind) {
		if strings.Contains(lowered, word) {
			return oneLineReason(sentence), true
		}
	}
	if mediaNamesAnotherKind(lowered, kind) {
		return "", false
	}
	for _, phrase := range mediaRejectionPhrases {
		if strings.Contains(lowered, phrase) {
			return oneLineReason(sentence), true
		}
	}
	return "", false
}

// mediaRejectionText is the prose a provider rejection is judged on — the
// error's own text plus the parsed body message, the same two sources
// IsImageRejection reads — with the sentence to remember quoted once. It
// reports false for anything that is not the provider answering 400 or 413.
func mediaRejectionText(err error) (text, sentence string, ok bool) {
	if err == nil {
		return "", "", false
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return "", "", false
	}
	switch apiErr.StatusCode {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
	default:
		return "", "", false
	}
	// Error() renders Err when the SDK set one; the parsed body text can carry
	// the sentence even then.
	text = err.Error()
	sentence = strings.TrimSpace(text)
	if message := strings.TrimSpace(apiErr.Message); message != "" {
		text += " " + message
		if sentence == "" {
			sentence = message
		}
	}
	text = strings.TrimSpace(text)
	return text, sentence, text != ""
}

// mediaKindWords are the words a provider's sentence uses to name one kind: the
// medium, and the wire part name a protocol gives it (which is what a provider
// quoting the rejected part prints).
func mediaKindWords(kind MediaKind) []string {
	switch kind {
	case MediaKindPDF:
		return []string{"pdf", "document"}
	case MediaKindAudio:
		return []string{"audio", "input_audio"}
	case MediaKindVideo:
		return []string{"video", "video_url"}
	}
	return nil
}

// mediaNamesAnotherKind reports whether the sentence is about a different
// native kind than the one asked about: a request can carry a PDF, a clip and a
// video at once, and a provider that names the audio is refusing the audio.
//
// An image mention does not count. Pictures have their own gate and their own
// heal, and the reporter sits above the heal: by the time a refusal is judged
// here the image it was about has been replaced with text, so what the sentence
// still says about pictures is what the model accepts, not what it refused.
func mediaNamesAnotherKind(lowered string, kind MediaKind) bool {
	for _, other := range []MediaKind{MediaKindPDF, MediaKindAudio, MediaKindVideo} {
		if other == kind {
			continue
		}
		for _, word := range mediaKindWords(other) {
			if strings.Contains(lowered, word) {
				return true
			}
		}
	}
	return false
}

// mediaRejectionPhrases are the generic prose a provider uses when it does not
// name the kind: the whole request is refused for what it carries.
var mediaRejectionPhrases = []string{
	"unsupported content type",
	"invalid content type",
	"does not support",
	"only supports text",
}

// NewMediaRefusalReporter returns a middleware that reports a provider's
// refusal of a native media part to whoever asked, and changes nothing else:
// the call still ends with the provider's error and is never retried here. The
// degraded resend belongs to the next turn (a refused kind is subtracted from
// what the conversation may send, see run.NativeMediaKinds), so a turn pays for
// a refusal of a kind nobody remembered exactly once.
func NewMediaRefusalReporter() LLMMiddleware {
	return func(next LLM) LLM {
		return &mediaRefusalReporterLLM{inner: next}
	}
}

type mediaRefusalReporterLLM struct {
	inner LLM
}

func (m *mediaRefusalReporterLLM) Execute(ctx context.Context, messages []Message, tools []*Tool) (*Result, error) {
	result, err := m.inner.Execute(ctx, messages, tools)
	if err != nil {
		reportMediaRefusals(ctx, messages, err)
	}
	return result, err
}

// reportMediaRefusals hands the observer one report per kind the failed request
// carried and the provider's answer names. A request carrying no such part, an
// error that is not a rejection, or no observer at all reports nothing — the
// scan runs only after a call failed, so a successful turn pays nothing.
func reportMediaRefusals(ctx context.Context, messages []Message, err error) {
	if MediaRefusalObserverFromContext(ctx) == nil {
		return
	}
	for _, kind := range mediaKindsInMessages(messages) {
		if reason, ok := IsMediaRejection(err, kind); ok {
			reportMediaRefusal(ctx, kind, reason)
		}
	}
}

// mediaKindsInMessages names the native-media kinds a request carries, each
// kind once however many parts carry it.
func mediaKindsInMessages(messages []Message) []MediaKind {
	var out []MediaKind
	seen := map[MediaKind]bool{}
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Type != ContentTypeFileBase64 {
				continue
			}
			kind := NativeMediaKindOfMIME(part.MIMEType)
			if kind == "" || seen[kind] {
				continue
			}
			seen[kind] = true
			out = append(out, kind)
		}
	}
	return out
}

// mediaRefusalObserverKey is the context key the observer rides on. A private
// type keeps the key from colliding with any other package's.
type mediaRefusalObserverKey struct{}

// WithMediaRefusalObserver runs fn once per provider refusal of a native media
// kind this call meets. It is how a caller that does know the provider and
// model learns a fact this package cannot name itself, exactly as
// WithImageRefusalObserver does for pictures: the reporter answers "what was
// refused", the observer answers "who should be told".
//
// The observer is a report, not a decision: it cannot change the request or the
// result, and it runs on the request's own goroutine. It must return quickly —
// a caller that has to write something does so on a context of its own, without
// blocking the turn's failure.
func WithMediaRefusalObserver(ctx context.Context, fn func(ctx context.Context, kind MediaKind, reason string)) context.Context {
	if ctx == nil || fn == nil {
		return ctx
	}
	return context.WithValue(ctx, mediaRefusalObserverKey{}, fn)
}

// MediaRefusalObserverFromContext returns the observer installed by
// WithMediaRefusalObserver, or nil when nobody asked to be told.
func MediaRefusalObserverFromContext(ctx context.Context) func(ctx context.Context, kind MediaKind, reason string) {
	if ctx == nil {
		return nil
	}
	fn, _ := ctx.Value(mediaRefusalObserverKey{}).(func(ctx context.Context, kind MediaKind, reason string))
	return fn
}

// reportMediaRefusal hands one refusal to the installed observer, with the
// request's own context. A nil observer is the ordinary case.
func reportMediaRefusal(ctx context.Context, kind MediaKind, reason string) {
	if fn := MediaRefusalObserverFromContext(ctx); fn != nil {
		fn(ctx, kind, reason)
	}
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
