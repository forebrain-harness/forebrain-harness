package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pdfBytes is a minimal file whose leading bytes are a PDF header; 04 — the
// sniffer reads the magic number, never the extension.
const pdfBytes = "%PDF-1.7\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n"

func TestNativeMediaKindOfMIME(t *testing.T) {
	cases := []struct {
		mime string
		want MediaKind
	}{
		{"application/pdf", MediaKindPDF},
		{"application/pdf; charset=binary", MediaKindPDF},
		{"audio/mpeg", MediaKindAudio},
		{"audio/wav", MediaKindAudio},
		{"video/mp4", MediaKindVideo},
		{"image/png", ""},
		{"text/plain", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := NativeMediaKindOfMIME(tc.mime); got != tc.want {
			t.Errorf("NativeMediaKindOfMIME(%q) = %q, want %q", tc.mime, got, tc.want)
		}
	}
}

func TestNativeMediaMIMETypeSniffsTheBytesNotTheName(t *testing.T) {
	dir := t.TempDir()
	pdf := filepath.Join(dir, "named.png")
	if err := os.WriteFile(pdf, []byte(pdfBytes), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := NativeMediaMIMETypeOfFile(pdf); got != "application/pdf" {
		t.Fatalf("NativeMediaMIMETypeOfFile = %q, want application/pdf (the extension must not decide)", got)
	}
	image := filepath.Join(dir, "real.png")
	if err := os.WriteFile(image, []byte("\x89PNG\r\n\x1a\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := NativeMediaMIMETypeOfFile(image); got != "" {
		t.Fatalf("an image is not native media: got %q", got)
	}
	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := NativeMediaMIMETypeOfFile(text); got != "" {
		t.Fatalf("text is not native media: got %q", got)
	}
}

func TestFileFromPathCarriesTheBytesAndItsSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo.pdf")
	if err := os.WriteFile(path, []byte(pdfBytes), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	part, err := FileFromPath(path)
	if err != nil {
		t.Fatalf("FileFromPath: %v", err)
	}
	if part.Type != ContentTypeFileBase64 || part.MIMEType != "application/pdf" || part.FileName != "demo.pdf" {
		t.Fatalf("part = %#v, want a file_base64 PDF named demo.pdf", part)
	}
	if part.FileBase64 != base64.StdEncoding.EncodeToString([]byte(pdfBytes)) {
		t.Fatalf("the bytes must be the file's base64")
	}
	if part.SourcePath != path {
		t.Fatalf("SourcePath = %q, want the file it was read from", part.SourcePath)
	}
}

func TestFileFromPathRefusesWhatItCannotSend(t *testing.T) {
	dir := t.TempDir()
	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := FileFromPath(text); err == nil {
		t.Fatal("a text file must not be sent as native media")
	}
	big := filepath.Join(dir, "big.pdf")
	head := []byte(pdfBytes)
	if err := os.WriteFile(big, append(head, make([]byte, NativeMediaInlineLimit)...), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := FileFromPath(big); !errors.Is(err, ErrNativeMediaTooLarge) {
		t.Fatalf("an oversize file must report ErrNativeMediaTooLarge, got %v", err)
	}
}

func TestNativeMediaTables(t *testing.T) {
	// What each protocol has an official shape for.
	protocols := map[string]NativeMediaKinds{
		ProtocolChatCompletions: {PDF: true, Audio: true, Video: true},
		ProtocolResponses:       {PDF: true},
		ProtocolMessages:        {PDF: true},
		ProtocolGemini:          {Audio: true, Video: true},
	}
	for protocol, want := range protocols {
		if got := NativeMediaForProtocol(protocol); got != want {
			t.Errorf("NativeMediaForProtocol(%q) = %+v, want %+v", protocol, got, want)
		}
	}
	if got := NativeMediaForProtocol("nonsense"); got.Any() {
		t.Errorf("an unknown protocol carries nothing, got %+v", got)
	}
	// What each provider's own endpoint documents.
	for _, provider := range []string{"deepseek", "qwen", "zhipu", "moonshot", "minimax", "tencent", "unknown"} {
		if got := NativeMediaForProvider(provider); got.Any() {
			t.Errorf("NativeMediaForProvider(%q) = %+v, want nothing: no official inline shape was verified", provider, got)
		}
	}
	if got := NativeMediaForProvider("volcengine"); got.PDF || !got.Audio || !got.Video {
		t.Errorf("volcengine = %+v, want audio+video (its Chat API documents no inline PDF)", got)
	}
	// The intersection is what a request may carry.
	model := NativeMediaKinds{PDF: true, Audio: true}
	if got := model.Every(NativeMediaForProtocol(ProtocolResponses)); got != (NativeMediaKinds{PDF: true}) {
		t.Errorf("model ∩ responses = %+v, want PDF only", got)
	}
	if got := model.Every(NativeMediaKinds{}); got.Any() {
		t.Errorf("nothing times anything is nothing, got %+v", got)
	}
}

func TestFilePartIsReportedWithoutItsBytes(t *testing.T) {
	part := FileBase64("demo.pdf", "application/pdf", strings.Repeat("A", 400))
	summary := FilePartSummary(part)
	if summary["mime_type"] != "application/pdf" || summary["file_name"] != "demo.pdf" {
		t.Fatalf("summary = %#v, want the type and the name", summary)
	}
	if summary["bytes"] != 300 {
		t.Fatalf("summary bytes = %v, want 300 (base64 decoded)", summary["bytes"])
	}
	for _, value := range summary {
		if text, ok := value.(string); ok && strings.Contains(text, "AAAA") {
			t.Fatalf("a summary must never carry the bytes: %#v", summary)
		}
	}
	if tokens := FilePartTokens(part); tokens != 75 {
		t.Fatalf("FilePartTokens = %d, want 75 (300 bytes / 4)", tokens)
	}
}

func TestNativeMediaTooLargeNoteNamesTheLimitAndNotThePath(t *testing.T) {
	note := NativeMediaTooLargeNote("demo.pdf", "application/pdf")
	if !strings.Contains(note, "demo.pdf") || !strings.Contains(note, "application/pdf") {
		t.Fatalf("note = %q, want the label and the type", note)
	}
	if strings.Contains(note, "inline") && strings.Contains(note, "/tmp") {
		t.Fatalf("note = %q, must not carry a path", note)
	}
}

// fieldMediaRejection is the shape a provider uses when it refuses the kind
// outright: the rejected part is named, no status but the 400 is implied.
const fieldMediaRejection = "Invalid content type: this model only accepts text and image input"

// TestIsMediaRejection pins the classifier the read side depends on: a hit
// switches a kind off for the rest of the conversation, so every rule is
// checked with the case it must refuse as well as the case it must catch —
// each kind is caught when named and when the provider answers with one of the
// generic sentences, and nothing is caught when the answer is not the
// provider's own 4xx, when it is about another kind, or when it says nothing.
func TestIsMediaRejection(t *testing.T) {
	rejection := func(status int, message string) error {
		return &APIError{StatusCode: status, Message: message}
	}
	cases := []struct {
		name   string
		err    error
		kind   MediaKind
		want   bool
		reason string
	}{
		// Each kind, named.
		{name: "pdf named", err: rejection(400, "unsupported pdf upload"), kind: MediaKindPDF, want: true, reason: "unsupported pdf"},
		{name: "document named", err: rejection(400, "the document type is not accepted here"), kind: MediaKindPDF, want: true, reason: "document type"},
		{name: "audio named", err: rejection(400, "unsupported audio format"), kind: MediaKindAudio, want: true, reason: "unsupported audio"},
		{name: "input_audio named", err: rejection(400, `unknown parameter: "input_audio"`), kind: MediaKindAudio, want: true},
		{name: "video named", err: rejection(400, "video input is not enabled for this model"), kind: MediaKindVideo, want: true, reason: "video input"},
		{name: "video_url named", err: rejection(400, `invalid "video_url" field`), kind: MediaKindVideo, want: true},
		// Each kind, through the generic sentences: the provider refuses the
		// request without naming what in it is unsupported.
		{name: "pdf generic", err: rejection(400, "unsupported content type"), kind: MediaKindPDF, want: true, reason: "unsupported content type"},
		{name: "audio generic", err: rejection(400, "this model does not support file input"), kind: MediaKindAudio, want: true},
		{name: "video generic", err: rejection(413, "only supports text"), kind: MediaKindVideo, want: true},
		{name: "pdf generic invalid content type", err: rejection(400, fieldMediaRejection), kind: MediaKindPDF, want: true},
		{name: "pdf beside an image mention", err: rejection(400, "does not support image input"), kind: MediaKindPDF, want: true},
		// Not the provider's own rejection: a server error, a rate limit, or
		// an error that never reached the provider. An outage must not be
		// remembered as "this model cannot take a PDF".
		{name: "500 naming the kind", err: rejection(500, "unsupported pdf"), kind: MediaKindPDF},
		{name: "503 naming the kind", err: rejection(503, "video input failed"), kind: MediaKindVideo},
		{name: "429 naming the kind", err: rejection(429, "audio rate limit"), kind: MediaKindAudio},
		{name: "timeout carrying the word", err: errors.New("send audio part: context deadline exceeded"), kind: MediaKindAudio},
		{name: "wrapped api error without status", err: rejection(0, "unsupported pdf"), kind: MediaKindPDF},
		// A 400 that says nothing about a kind.
		{name: "ambiguous 400", err: rejection(400, "invalid request: unknown parameter 'metadata'"), kind: MediaKindPDF},
		{name: "empty message", err: &APIError{StatusCode: 400}, kind: MediaKindPDF},
		{name: "nil error", err: nil, kind: MediaKindPDF},
		// About something else that the request can carry.
		{name: "another kind", err: rejection(400, "unsupported audio"), kind: MediaKindPDF},
		{name: "another kind generic", err: rejection(400, "does not support video"), kind: MediaKindAudio},
		{name: "another kind named in passing", err: rejection(400, "unsupported pdf"), kind: MediaKindAudio},
		{name: "an image only", err: rejection(400, "unsupported image"), kind: MediaKindVideo},
		// No kind at all.
		{name: "no kind", err: rejection(400, "unsupported content type"), kind: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := IsMediaRejection(tc.err, tc.kind)
			if ok != tc.want {
				t.Fatalf("IsMediaRejection(%v, %q) = %v, %v; want match=%v", tc.err, tc.kind, ok, reason, tc.want)
			}
			if !ok {
				if reason != "" {
					t.Fatalf("a miss must carry no reason, got %q", reason)
				}
				return
			}
			if strings.TrimSpace(reason) == "" {
				t.Fatal("a hit must carry the provider's sentence")
			}
			if strings.ContainsAny(reason, "\n\r") {
				t.Fatalf("reason = %q, want one line", reason)
			}
			if tc.reason != "" && !strings.Contains(reason, tc.reason) {
				t.Fatalf("reason = %q, want it to carry %q", reason, tc.reason)
			}
		})
	}
}

// refusingMediaLLM refuses the request when it carries a native media part the
// provider does not take, the way a real endpoint does: the whole request is
// rejected, never a single part.
type refusingMediaLLM struct {
	calls int
	err   error
}

func (l *refusingMediaLLM) Execute(_ context.Context, messages []Message, _ []*Tool) (*Result, error) {
	l.calls++
	if len(mediaKindsInMessages(messages)) > 0 {
		return nil, l.err
	}
	return &Result{Message: &Message{Role: RoleAssistant, Parts: []ContentPart{Text("read it")}}}, nil
}

// TestMediaRefusalReporter pins the report and, just as important, what it
// must not do: it names the kind the provider's sentence names, once per kind
// the request carries, and it never retries — the call still fails with the
// provider's own error, because degrading the request is the next turn's
// decision.
func TestMediaRefusalReporter(t *testing.T) {
	t.Parallel()
	request := []Message{{Role: RoleUser, Parts: []ContentPart{
		Text("summarize these"),
		FileBase64("report.pdf", "application/pdf", strings.Repeat("A", 32)),
		FileBase64("call.mp3", "audio/mpeg", strings.Repeat("A", 32)),
	}}}
	refusal := &APIError{StatusCode: 400, Message: "unsupported pdf upload"}
	inner := &refusingMediaLLM{err: refusal}
	reported := map[MediaKind]string{}
	ctx := WithMediaRefusalObserver(context.Background(), func(_ context.Context, kind MediaKind, reason string) {
		reported[kind] = reason
	})
	_, err := Use(inner, NewMediaRefusalReporter()).Execute(ctx, request, nil)
	if !errors.Is(err, refusal) {
		t.Fatalf("err = %v, want the provider's own error unchanged", err)
	}
	if inner.calls != 1 {
		t.Fatalf("calls = %d, want 1 — a refused media part is never retried this turn", inner.calls)
	}
	if len(reported) != 1 || !strings.Contains(reported[MediaKindPDF], "unsupported pdf") {
		t.Fatalf("reported = %v, want the PDF the provider named and nothing else", reported)
	}

	// A request that carries no native media reports nothing, and no observer
	// at all is the ordinary case.
	reported = map[MediaKind]string{}
	clean := []Message{{Role: RoleUser, Parts: []ContentPart{Text("no files here")}}}
	if _, err := Use(&refusingMediaLLM{err: refusal}, NewMediaRefusalReporter()).Execute(ctx, clean, nil); err != nil {
		t.Fatalf("the clean request failed: %v", err)
	}
	if len(reported) != 0 {
		t.Fatalf("reported = %v on a request with no media", reported)
	}
	if _, err := Use(inner, NewMediaRefusalReporter()).Execute(context.Background(), request, nil); err == nil {
		t.Fatal("a refusing provider must still fail the call")
	}
}

// TestNativeMediaKindsWithout pins the set subtraction the read side applies: a
// refused kind leaves, and the other kinds are untouched.
func TestNativeMediaKindsWithout(t *testing.T) {
	all := NativeMediaKinds{PDF: true, Audio: true, Video: true}
	got := all.Without(NativeMediaKinds{PDF: true})
	if got.PDF || !got.Audio || !got.Video {
		t.Fatalf("Without(pdf) = %+v, want audio and video only", got)
	}
	if got := all.Without(NativeMediaKinds{}); got != all {
		t.Fatalf("Without(nothing) = %+v, want the set unchanged", got)
	}
	if got := all.Without(all); got.Any() {
		t.Fatalf("Without(everything) = %+v, want nothing", got)
	}
}
