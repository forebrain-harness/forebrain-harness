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
// the file a reference points at — and labels each with its message, in
// request order. The label is the key joining a picture to its file: the
// review's prompt names the same label with the file that image is stored in,
// so a path is said once, in the text the reviewer and the review's transcript
// read. The file is what lets the reviewer look closer than the picture it is
// sent: crop or enlarge it with a script to read small text. An image read
// from a file is persisted by reference, so the review's transcript does not
// store its bytes again. A reference whose file is gone carries no image and
// adds nothing, as in history.
//
// The sources come back with each message's Files filled in, in the order its
// images were read: the caller builds the prompt from them.
func planReviewImageParts(ctx context.Context, runner *run.Runner, sources []turn.RequestImage) ([]llm.ContentPart, []turn.RequestImage) {
	if runner == nil || len(sources) == 0 {
		return nil, nil
	}
	var out []llm.ContentPart
	filled := make([]turn.RequestImage, 0, len(sources))
	for _, source := range sources {
		n := 0
		var files []string
		parts := run.UserInputParts(ctx, runner.FileResolver, source.PartsJSON)
		for _, part := range parts {
			if part.Type != llm.ContentTypeImageBase64 && part.Type != llm.ContentTypeImageURL {
				continue
			}
			n++
			out = append(out, llm.Text(fmt.Sprintf("Image %d of message %d:", n, source.Message)), part)
			files = append(files, run.ImageSource(part))
		}
		source.Files = files
		filled = append(filled, source)
	}
	return out, filled
}
