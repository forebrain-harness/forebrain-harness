package anthropic

import (
	"errors"
	"net/http"
	"testing"

	anthropicapi "github.com/anthropics/anthropic-sdk-go"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
)

func TestNormalizeErrorMapsStatusWithoutRenderingText(t *testing.T) {
	t.Parallel()

	// Request and Response are deliberately left nil: the SDK's Error() panics
	// without them, which is exactly why normalizing must not stringify the
	// error eagerly.
	in := &anthropicapi.Error{StatusCode: http.StatusRequestEntityTooLarge}
	var got *llm.APIError
	if !errors.As(normalizeError(in), &got) || got == nil {
		t.Fatal("an anthropic Error must normalize to *llm.APIError")
	}
	if got.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("StatusCode = %d, want 413", got.StatusCode)
	}
	if got.Message != "" {
		t.Fatalf("Message = %q, want empty: the SDK text must stay lazy", got.Message)
	}
	if !errors.Is(normalizeError(in), error(in)) {
		t.Fatal("the original SDK error must stay reachable")
	}
	// 413 is decidable from the status alone, so this must classify without
	// ever rendering the SDK's text.
	if !llm.IsExceeded(normalizeError(in)) {
		t.Fatal("a 413 must still classify as exceeded after normalizing")
	}
}

func TestNormalizeErrorLeavesForeignErrorsAlone(t *testing.T) {
	t.Parallel()

	if normalizeError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	plain := errors.New("connection refused")
	if got := normalizeError(plain); got != plain {
		t.Fatalf("a non-SDK error must pass through untouched, got %#v", got)
	}
}
