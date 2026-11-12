package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

const compatTestPDF = "%PDF-1.7\n1 0 obj\n<<>>\nendobj\n%%EOF\n"

// captureCompatBody runs one non-streaming Execute against a fake server and
// returns the request body it received.
func captureCompatBody(t *testing.T, messages []llm.Message) ([]byte, error) {
	t.Helper()
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(server.Close)
	client := NewCompatLLMWithPromptCaching("sk-test", server.URL+"/v1", "test-model", 0, 64, "", []byte(`{"stream":false}`), false)
	_, err := client.Execute(context.Background(), messages, nil)
	return body, err
}

// decodedMessages is the request body's messages array as JSON objects.
func decodedMessages(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var payload struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode request body: %v\n%s", err, body)
	}
	return payload.Messages
}

func contentParts(t *testing.T, message map[string]any) []map[string]any {
	t.Helper()
	raw, ok := message["content"].([]any)
	if !ok {
		t.Fatalf("message content is not a part list: %#v", message)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		part, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("content part is not an object: %#v", item)
		}
		out = append(out, part)
	}
	return out
}

// A PDF is sent as the Chat Completions `file` part: a data URL plus the name,
// with no trace of the image slot the legacy content part type forced.
func TestCompatSendsAPDFAsAFilePart(t *testing.T) {
	part := llm.FileBase64("demo.pdf", "application/pdf", "JVBERi0xLjcK")
	body, err := captureCompatBody(t, []llm.Message{llm.UserMessage(llm.Text("read this"), part)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	parts := contentParts(t, decodedMessages(t, body)[0])
	var file map[string]any
	for _, p := range parts {
		if p["type"] == "file" {
			file, _ = p["file"].(map[string]any)
		}
	}
	if file == nil {
		t.Fatalf("no file part in %s", body)
	}
	if file["file_data"] != "data:application/pdf;base64,JVBERi0xLjcK" {
		t.Fatalf("file_data = %v, want the data URL", file["file_data"])
	}
	if file["filename"] != "demo.pdf" {
		t.Fatalf("filename = %v, want demo.pdf", file["filename"])
	}
	if strings.Contains(string(body), "name=") || strings.Contains(string(body), "image_url\":{\"url\":\"data:application/pdf") {
		t.Fatalf("the data-URL parameter and the image slot must not reach the wire: %s", body)
	}
}

// Audio is the `input_audio` part: bare base64 and the format the API names.
func TestCompatSendsAudioAsInputAudio(t *testing.T) {
	cases := []struct{ mime, format string }{
		{"audio/mpeg", "mp3"},
		{"audio/mp3", "mp3"},
		{"audio/wav", "wav"},
		{"audio/x-wav", "wav"},
	}
	for _, tc := range cases {
		t.Run(tc.mime, func(t *testing.T) {
			part := llm.FileBase64("clip", tc.mime, "UklGRg==")
			body, err := captureCompatBody(t, []llm.Message{llm.UserMessage(llm.Text("listen"), part)})
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			var audio map[string]any
			for _, p := range contentParts(t, decodedMessages(t, body)[0]) {
				if p["type"] == "input_audio" {
					audio, _ = p["input_audio"].(map[string]any)
				}
			}
			if audio == nil {
				t.Fatalf("no input_audio part in %s", body)
			}
			if audio["data"] != "UklGRg==" || audio["format"] != tc.format {
				t.Fatalf("input_audio = %#v, want data + format %s", audio, tc.format)
			}
		})
	}
}

// A format the API has no name for is refused before the bytes leave, with the
// sentence that says what to do — never sent under a guessed name.
func TestCompatRefusesAnAudioFormatTheAPIDoesNotName(t *testing.T) {
	part := llm.FileBase64("clip.m4a", "audio/mp4", "AAAA")
	if _, err := captureCompatBody(t, []llm.Message{llm.UserMessage(llm.Text("listen"), part)}); err == nil {
		t.Fatal("audio/mp4 must be refused, not sent as wav or mp3")
	} else if !strings.Contains(err.Error(), "wav or mp3") {
		t.Fatalf("error = %v, want it to name the formats the API carries", err)
	}
}

// Video is the dialect shape 火山方舟 documents: `video_url` holds the data URL
// itself.
func TestCompatSendsVideoAsAVideoURLDataURL(t *testing.T) {
	part := llm.FileBase64("clip.mp4", "video/mp4", "AAAAIGZ0eXA=")
	body, err := captureCompatBody(t, []llm.Message{llm.UserMessage(llm.Text("watch"), part)})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var video map[string]any
	for _, p := range contentParts(t, decodedMessages(t, body)[0]) {
		if p["type"] == "video_url" {
			video = p
		}
	}
	if video == nil {
		t.Fatalf("no video_url part in %s", body)
	}
	if video["video_url"] != "data:video/mp4;base64,AAAAIGZ0eXA=" {
		t.Fatalf("video_url = %v, want the data URL as a string", video["video_url"])
	}
}

// The switch-off path: a request that carries no file part is sent as the exact
// bytes the projection wrote — the rewrite never runs.
func TestCompatNativeMediaRoundTripperLeavesOtherBodiesAlone(t *testing.T) {
	payload := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]}`
	var got []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	rt := &compatNativeMediaRoundTripper{inner: http.DefaultTransport}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()
	if !bytes.Equal(got, []byte(payload)) {
		t.Fatalf("an image-only request was rewritten:\n got %s\nwant %s", got, payload)
	}
}

// The data URL the projection writes keeps the name in a parameter the transport
// strips: what a provider sees is always data:<type>;base64,<bytes>.
func TestCompatNativeMediaDataURLRoundTripsTheName(t *testing.T) {
	part := llm.FileBase64("my report;v2.pdf", "application/pdf", "JVBERi0=")
	dataURL := compatNativeMediaDataURL(part)
	if !strings.HasPrefix(dataURL, "data:application/pdf;name=") {
		t.Fatalf("data URL = %q, want the media type then the name", dataURL)
	}
	name, encoded, bare, err := compatDataURLParts(dataURL)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if name != "my report;v2.pdf" {
		t.Fatalf("name = %q, want the whole name back", name)
	}
	if encoded != "JVBERi0=" || bare != "data:application/pdf;base64,JVBERi0=" {
		t.Fatalf("bytes = %q / %q", encoded, bare)
	}
}

// An image part still goes as an image: the marker scan must not catch it.
func TestCompatNativeMediaBodyScanIgnoresImages(t *testing.T) {
	if compatNativeMediaBodyHasFilePart([]byte(`data:image/png;base64,AAAA`)) {
		t.Fatal("an image data URL is not a file part")
	}
	for _, body := range []string{`data:application/pdf;base64,AA`, `data:audio/mpeg;base64,AA`, `data:video/mp4;base64,AA`} {
		if !compatNativeMediaBodyHasFilePart([]byte(body)) {
			t.Fatalf("%s must be recognized as a file part", body)
		}
	}
}
