package llm

import (
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
