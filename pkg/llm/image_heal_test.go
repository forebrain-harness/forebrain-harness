package llm

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// TestUnsendableImageNoteIsOneLine pins the note's shape: it is one prompt-safe
// line, it names the file and type when it knows them, and a provider sentence
// that arrives with newlines or at great length cannot turn the prompt into a
// paragraph.
func TestUnsendableImageNoteIsOneLine(t *testing.T) {
	t.Parallel()
	got := UnsendableImageNote("shot.png", "image/png", "line one\n\tline   two")
	want := "[attachment] shot.png (image/png) — not sent: line one line two"
	if got != want {
		t.Fatalf("note = %q want %q", got, want)
	}

	// No type known: no empty parentheses.
	got = UnsendableImageNote("shot.png", "", "unsupported")
	if got != "[attachment] shot.png — not sent: unsupported" {
		t.Fatalf("note without mime = %q", got)
	}

	// A long reason is cut with an ellipsis, not truncated silently.
	long := strings.Repeat("x", 400)
	got = UnsendableImageNote("shot.png", "image/png", long)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("long reason not marked as cut: %q", got)
	}
	if len(got) > len("[attachment] shot.png (image/png) — not sent: ")+unsendableNoteReasonLimit+len("…") {
		t.Fatalf("note = %d bytes, want the reason capped at %d", len(got), unsendableNoteReasonLimit)
	}
}

// fieldImageRejection is this provider's answer to the screenshot that started
// this work, verbatim from the error log: the message names image formats and
// never mentions size, which is why an overlong side looks like a broken file.
const fieldImageRejection = ".messages[353].image[0]: You have uploaded an unsupported image. " +
	"Please make sure your image is valid and has one of the following formats: webp, png, jpeg, and gif."

// TestIsImageRejectionMatchesTheFieldError pins the predicate on the real
// error, and pins its two edges: a server error is never an image complaint
// (retrying it without the image would hide an outage), and an unrelated
// client error is not one either.
func TestIsImageRejectionMatchesTheFieldError(t *testing.T) {
	t.Parallel()
	if !IsImageRejection(&APIError{StatusCode: 400, Message: fieldImageRejection}) {
		t.Fatal("the field 400 was not recognized as an image rejection")
	}
	if !IsImageRejection(&APIError{StatusCode: 413, Message: "invalid_image: too large"}) {
		t.Fatal("a 413 invalid_image was not recognized")
	}
	if IsImageRejection(&APIError{StatusCode: 500, Message: "unsupported image"}) {
		t.Fatal("a 500 must not be treated as an image complaint")
	}
	if IsImageRejection(errors.New("context_length_exceeded")) {
		t.Fatal("a context-length error was treated as an image complaint")
	}
	if IsImageRejection(nil) {
		t.Fatal("nil was treated as an image complaint")
	}
}

func countImageParts(messages []Message) int {
	n := 0
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if part.Type == ContentTypeImageURL || part.Type == ContentTypeImageBase64 {
				n++
			}
		}
	}
	return n
}

// TestDegradeImagesKeepsTextAndNamesTheDrop pins the rewrite: every image
// becomes one deterministic text part that says what was dropped and why, the
// text around it survives, and the caller's messages are untouched — the
// transcript must keep its images, only the retried request loses them.
func TestDegradeImagesKeepsTextAndNamesTheDrop(t *testing.T) {
	t.Parallel()
	messages := []Message{
		{Role: RoleSystem, Parts: []ContentPart{Text("be terse")}},
		{Role: RoleUser, Parts: []ContentPart{Text("what is this?"), ImageBase64("image/png", strings.Repeat("A", 4096))}},
	}

	degraded, replaced := DegradeImages(messages, "provider said no")
	if replaced != 1 {
		t.Fatalf("replaced = %d want 1", replaced)
	}
	if countImageParts(degraded) != 0 {
		t.Fatal("an image survived the degradation")
	}
	if degraded[1].Parts[0].Text != "what is this?" {
		t.Fatalf("text part changed: %q", degraded[1].Parts[0].Text)
	}
	note := degraded[1].Parts[1].Text
	for _, want := range []string{"[image omitted:", "image/png", "KB", "provider said no"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note = %q want it to contain %q", note, want)
		}
	}
	if countImageParts(messages) != 1 {
		t.Fatal("DegradeImages mutated the caller's messages; the transcript must keep its images")
	}
	// Deterministic: the same input produces the same note, so a retry cannot
	// perturb the request beyond removing the image.
	again, _ := DegradeImages(messages, "provider said no")
	if again[1].Parts[1].Text != note {
		t.Fatal("DegradeImages is not deterministic")
	}
}

// imageRejectingLLM refuses any request that carries an image with the field
// error and answers anything textual, recording what it was sent each time.
type imageRejectingLLM struct {
	calls  int
	images []int
}

func (l *imageRejectingLLM) Execute(_ context.Context, messages []Message, _ []*Tool) (*Result, error) {
	l.calls++
	l.images = append(l.images, countImageParts(messages))
	if countImageParts(messages) > 0 {
		return nil, &APIError{StatusCode: 400, Message: fieldImageRejection}
	}
	return &Result{Message: &Message{Role: RoleAssistant, Parts: []ContentPart{Text("a screenshot")}}}, nil
}

// TestNewImageHealRetriesOnceWithDegradedImages pins the last resort: the first
// request carries the image and is refused; the retry carries the same
// conversation with the image replaced by text and succeeds. Exactly one retry
// — a second failure is the caller's to handle.
func TestNewImageHealRetriesOnceWithDegradedImages(t *testing.T) {
	t.Parallel()
	inner := &imageRejectingLLM{}
	healed := Use(inner, NewImageHeal())

	messages := []Message{
		{Role: RoleUser, Parts: []ContentPart{Text("what is this?"), ImageBase64("image/png", strings.Repeat("A", 4096))}},
	}
	res, err := healed.Execute(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("the healed request failed: %v", err)
	}
	if res == nil || res.Message == nil {
		t.Fatalf("res = %+v want the assistant message", res)
	}
	if inner.calls != 2 {
		t.Fatalf("calls = %d want exactly 2 (one refused, one degraded)", inner.calls)
	}
	if inner.images[0] != 1 || inner.images[1] != 0 {
		t.Fatalf("images per call = %v want [1 0]", inner.images)
	}
}

// TestImageHealRefusesToResendUnsafeRequests pins the plan 123 guard: a
// refusal that arrives after the request already finished an output block is
// not retried with the image degraded — the caller finishes from what
// completed instead.
func TestImageHealRefusesToResendUnsafeRequests(t *testing.T) {
	t.Parallel()
	inner := &imageRejectingLLM{}
	healed := Use(inner, NewImageHeal())

	sink := &StreamSink{
		OnDelta:         func(string) {},
		OnTextBlockDone: func() {},
	}
	ctx, _ := WithStreamProgress(WithStreamSink(context.Background(), sink))
	// Drive the record through the wrapped sink the request itself would use.
	wrapped := StreamSinkFrom(ctx)
	wrapped.OnDelta("the answer that arrived")
	wrapped.OnTextBlockDone()

	messages := []Message{
		{Role: RoleUser, Parts: []ContentPart{Text("what is this?"), ImageBase64("image/png", strings.Repeat("A", 4096))}},
	}
	_, err := healed.Execute(ctx, messages, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("Execute error = %v, want the original image rejection", err)
	}
	if inner.calls != 1 {
		t.Fatalf("calls = %d, want 1 (the request was not resend-safe)", inner.calls)
	}
}

// TestImageHealReportsTheRefusalOnce pins the observer: a caller that knows the
// provider and model (pkg/run) learns the refusal exactly once, with the
// provider's sentence flattened to one line, and the retry itself is unchanged —
// the observer only reports.
func TestImageHealReportsTheRefusalOnce(t *testing.T) {
	t.Parallel()
	inner := &imageRejectingLLM{}
	healed := Use(inner, NewImageHeal())

	var reasons []string
	ctx := WithImageRefusalObserver(context.Background(), func(_ context.Context, reason string) { reasons = append(reasons, reason) })
	messages := []Message{
		{Role: RoleUser, Parts: []ContentPart{Text("what is this?"), ImageBase64("image/png", strings.Repeat("A", 4096))}},
	}
	if _, err := healed.Execute(ctx, messages, nil); err != nil {
		t.Fatalf("the healed request failed: %v", err)
	}
	if len(reasons) != 1 {
		t.Fatalf("observer ran %d times, want once per refusal: %v", len(reasons), reasons)
	}
	if !strings.Contains(reasons[0], "unsupported image") || strings.ContainsAny(reasons[0], "\n\r") {
		t.Fatalf("reason = %q, want the provider's sentence on one line", reasons[0])
	}
	// Reporting is not deciding: the retry still happened, degraded.
	if inner.calls != 2 || inner.images[1] != 0 {
		t.Fatalf("calls=%d images=%v, want the same retry an unobserved heal makes", inner.calls, inner.images)
	}

	// A request the provider accepts reports nothing.
	reasons = nil
	clean := []Message{{Role: RoleUser, Parts: []ContentPart{Text("no pictures here")}}}
	if _, err := healed.Execute(ctx, clean, nil); err != nil {
		t.Fatalf("the second request failed: %v", err)
	}
	if len(reasons) != 0 {
		t.Fatalf("observer ran %d times on a request with no image", len(reasons))
	}
}

// TestImageHealWithoutAnObserverIsUnchanged pins the two shapes of "nobody
// asked": no observer at all, and a nil one. Neither may change the heal, and a
// nil may not panic.
func TestImageHealWithoutAnObserverIsUnchanged(t *testing.T) {
	t.Parallel()
	messages := []Message{
		{Role: RoleUser, Parts: []ContentPart{Text("what is this?"), ImageBase64("image/png", strings.Repeat("A", 4096))}},
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "no observer", ctx: context.Background()},
		{name: "nil observer", ctx: WithImageRefusalObserver(context.Background(), nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inner := &imageRejectingLLM{}
			res, err := Use(inner, NewImageHeal()).Execute(tc.ctx, messages, nil)
			if err != nil {
				t.Fatalf("the healed request failed: %v", err)
			}
			if res == nil || res.Message == nil {
				t.Fatalf("res = %+v want the assistant message", res)
			}
			if inner.calls != 2 || inner.images[0] != 1 || inner.images[1] != 0 {
				t.Fatalf("calls=%d images=%v, want the ordinary heal", inner.calls, inner.images)
			}
			if ImageRefusalObserverFromContext(tc.ctx) != nil {
				t.Fatal("a nil observer must not be installed")
			}
		})
	}
}
