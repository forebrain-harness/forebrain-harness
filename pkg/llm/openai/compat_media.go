package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

// Chat Completions native media.
//
// The legacy go-openai content part this client sends through has two fields
// and no more: a type, and an image_url. A PDF, an audio clip and a video have
// their own parts in the protocol — `file`, `input_audio`, `video_url` — so the
// projection writes every file part as a data URL in the image slot and
// compatNativeMediaRoundTripper turns those into the shape the protocol names,
// at the last moment before the body leaves the process. Two consequences are
// deliberate:
//
//   - A request that carries no file part is sent as the exact bytes go-openai
//     wrote. The rewrite only fires when it finds one, so every request of a
//     conversation that attaches none — and every request before this plan
//     existed — is byte-for-byte what it was.
//   - A protocol shape is spelled out once, in nativeMediaPayload, where the
//     official documents recorded in docs/plan/NATIVE_MEDIA_INPUT_FACTS.md are
//     quoted. Nothing above this file needs to know which field carries a PDF.

// compatNativeMediaMarker is the head of the data URL the projection writes for
// a file part. Reading the body for it is what keeps an ordinary request from
// being unmarshalled and re-encoded at all.
const compatNativeMediaMarker = "data:application/pdf;"

// compatNativeMediaDataURL renders one file part as the data URL the projection
// puts in the image slot. The file's name rides as a data-URL parameter because
// the part being written has no field for it: the transport moves it into the
// provider's own `filename` and strips it from the URL, so what is sent is
// always `data:<media type>;base64,<bytes>`.
func compatNativeMediaDataURL(part llm.ContentPart) string {
	header := "data:" + strings.TrimSpace(part.MIMEType)
	if name := strings.TrimSpace(part.FileName); name != "" {
		header += ";name=" + url.QueryEscape(name)
	}
	return header + ";base64," + strings.TrimSpace(part.FileBase64)
}

// compatNativeMediaRoundTripper rewrites the file parts of a Chat Completions
// request body into the protocol's own shapes.
type compatNativeMediaRoundTripper struct {
	inner http.RoundTripper
}

func (t *compatNativeMediaRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	inner := t.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	if req == nil || req.Method != http.MethodPost || req.URL == nil || req.Body == nil ||
		!strings.Contains(req.URL.Path, "chat/completions") {
		return inner.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	// The request is sent as it was when nothing below matches, so its body is
	// put back before any of the ways out.
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	if err != nil {
		return nil, err
	}
	if !compatNativeMediaBodyHasFilePart(body) {
		return inner.RoundTrip(req)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("native media: decode request body: %w", err)
	}
	rewritten, err := compatNativeMediaRewriteMessages(payload)
	if err != nil {
		return nil, err
	}
	if !rewritten {
		return inner.RoundTrip(req)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("native media: encode request body: %w", err)
	}
	clone := req.Clone(req.Context())
	clone.Body = io.NopCloser(bytes.NewReader(encoded))
	clone.ContentLength = int64(len(encoded))
	clone.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(encoded)), nil
	}
	return inner.RoundTrip(clone)
}

// compatNativeMediaBodyHasFilePart reports whether body can carry one of the
// data URLs the projection writes for a file. It is a byte scan rather than a
// decode: every request of a conversation pays for it, and all but the ones that
// attach a PDF, an audio clip or a video must cost nothing.
func compatNativeMediaBodyHasFilePart(body []byte) bool {
	for _, prefix := range []string{compatNativeMediaMarker, "data:audio/", "data:video/"} {
		if bytes.Contains(body, []byte(prefix)) {
			return true
		}
	}
	return false
}

// compatNativeMediaRewriteMessages replaces every file part of every message
// with the shape its media type has, reporting whether it changed anything.
func compatNativeMediaRewriteMessages(payload map[string]any) (bool, error) {
	messages, ok := payload["messages"].([]any)
	if !ok {
		return false, nil
	}
	rewritten := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		parts, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for i, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if !ok || strings.TrimSpace(asString(part["type"])) != "image_url" {
				continue
			}
			imageURL, ok := part["image_url"].(map[string]any)
			if !ok {
				continue
			}
			dataURL := strings.TrimSpace(asString(imageURL["url"]))
			mimeType := compatDataURLMediaType(dataURL)
			kind := llm.NativeMediaKindOfMIME(mimeType)
			if kind == "" {
				continue // an image: what the slot always carried
			}
			native, err := compatNativeMediaPayload(kind, dataURL, mimeType)
			if err != nil {
				return false, err
			}
			parts[i] = native
			rewritten = true
		}
		message["content"] = parts
	}
	return rewritten, nil
}

// compatDataURLMediaType is the media type of a data URL, "" for anything else.
func compatDataURLMediaType(dataURL string) string {
	if !strings.HasPrefix(dataURL, "data:") {
		return ""
	}
	header, _, ok := strings.Cut(strings.TrimPrefix(dataURL, "data:"), ",")
	if !ok {
		return ""
	}
	mediaType, _, _ := strings.Cut(header, ";")
	return strings.ToLower(strings.TrimSpace(mediaType))
}

// compatDataURLParts splits the data URL the projection wrote into the file's
// name and its bare base64, and the URL the protocol wants back.
func compatDataURLParts(dataURL string) (name, base64Data, bare string, err error) {
	header, payload, ok := strings.Cut(strings.TrimPrefix(dataURL, "data:"), ",")
	if !ok {
		return "", "", "", errors.New("native media: a file part is not a data URL")
	}
	fields := strings.Split(header, ";")
	mediaType := strings.TrimSpace(fields[0])
	isBase64 := false
	for _, field := range fields[1:] {
		field = strings.TrimSpace(field)
		switch {
		case field == "base64":
			isBase64 = true
		case strings.HasPrefix(field, "name="):
			decoded, decodeErr := url.QueryUnescape(strings.TrimPrefix(field, "name="))
			if decodeErr == nil {
				name = decoded
			}
		}
	}
	if !isBase64 || strings.TrimSpace(payload) == "" {
		return "", "", "", errors.New("native media: a file part carries no base64 bytes")
	}
	return name, payload, "data:" + mediaType + ";base64," + payload, nil
}

// compatNativeMediaPayload renders one file part in the shape Chat Completions
// documents for its media type:
//
//   - a PDF as a `file` part whose file_data is the data URL
//     (https://github.com/openai/openai-python/blob/main/src/openai/types/chat/chat_completion_content_part_param.py)
//   - audio as an `input_audio` part carrying the bare base64 and the format
//     the API names, wav or mp3
//     (https://github.com/openai/openai-python/blob/main/src/openai/types/chat/chat_completion_content_part_input_audio_param.py)
//   - a video as a `video_url` part holding the data URL, the shape 火山方舟
//     (Doubao) documents for its Chat API
//     (https://docs.volcengine.com/docs/ark/video-understanding)
//
// A kind with no shape here is an error rather than a dropped part: the assembly
// only builds these parts for a model and protocol that carry them, so one
// arriving here is a defect, and a request that silently lost its attachment is
// worse than one that says why it cannot be sent.
func compatNativeMediaPayload(kind llm.MediaKind, dataURL, mimeType string) (map[string]any, error) {
	name, base64Data, bare, err := compatDataURLParts(dataURL)
	if err != nil {
		return nil, err
	}
	switch kind {
	case llm.MediaKindPDF:
		file := map[string]any{"file_data": bare}
		if name != "" {
			file["filename"] = name
		}
		return map[string]any{"type": "file", "file": file}, nil
	case llm.MediaKindAudio:
		format, err := compatAudioFormat(mimeType)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"type":        "input_audio",
			"input_audio": map[string]any{"data": base64Data, "format": format},
		}, nil
	case llm.MediaKindVideo:
		return map[string]any{"type": "video_url", "video_url": bare}, nil
	}
	return nil, fmt.Errorf("native media: Chat Completions has no part for a %q file", string(kind))
}

// compatAudioFormat names the two audio formats Chat Completions documents. Any
// other one is refused here, before the bytes are sent, with the sentence a
// reader can act on — the API has no name for it, so it cannot be sent under a
// guessed one.
func compatAudioFormat(mimeType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "audio/mpeg", "audio/mp3":
		return "mp3", nil
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "wav", nil
	}
	return "", fmt.Errorf("native media: Chat Completions carries audio as wav or mp3, not %s; convert the file or detach it", strings.TrimSpace(mimeType))
}

// asString reads a decoded JSON string field, "" for anything else.
func asString(v any) string {
	s, _ := v.(string)
	return s
}
