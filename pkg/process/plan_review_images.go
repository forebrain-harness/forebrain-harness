package process

import (
	"context"
	"fmt"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/run"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// planReviewImageParts reads the images of the messages a review request
// names back the way the conversation's history reads them — inline, or from
// the file a reference points at — labels each with its message, and keeps
// the line naming its file (llm.ImageSourceNote) after it, in request order.
// The path is what lets the reviewer look closer than the picture it is sent:
// crop or enlarge it with a script to read small text. An image read from a
// file is persisted by reference, so the review's transcript does not store
// its bytes again. A reference whose file is gone carries no image and adds
// nothing, as in history.
func planReviewImageParts(ctx context.Context, runner *run.Runner, sources []turn.RequestImage) []llm.ContentPart {
	if runner == nil || len(sources) == 0 {
		return nil
	}
	var out []llm.ContentPart
	for _, source := range sources {
		n := 0
		parts := run.UserInputParts(ctx, runner.FileResolver, source.PartsJSON)
		for i, part := range parts {
			if part.Type != llm.ContentTypeImageBase64 && part.Type != llm.ContentTypeImageURL {
				continue
			}
			n++
			out = append(out, llm.Text(fmt.Sprintf("Image %d of message %d:", n, source.Message)), part)
			// The line naming its file comes with it, exactly as history
			// carries it — so the review's own replay sends the same parts.
			if i+1 < len(parts) && llm.IsImageSourceNote(parts[i+1]) {
				out = append(out, parts[i+1])
			}
		}
	}
	return out
}
