// ChatGPT: the subscription a ChatGPT plan includes, reached by signing in to
// the account itself. The login is the credential — the Codex backend owns the
// endpoint and the tokens — so a row this subscription writes carries no key.
package subscription

import (
	"context"
	"fmt"
	"io"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// chatGPTID is the provider id a ChatGPT row carries, and the source recorded
// on every row this subscription writes.
const chatGPTID = "chatgpt"

// The flow, the probe and the listing, as variables so a test drives them
// without a browser and without talking to auth.openai.com or the Codex
// backend. Production runs the same openai functions the terminal's /connect
// and the web login run, so one sign-in serves every surface.
var (
	chatGPTLogin = openai.LoginWithManualCallback
	chatGPTProbe = func(ctx context.Context, home string) turn.SignInCheck {
		return turn.ProbeChatGPTModels(ctx, openai.CredentialsPath(home), process.NewChatGPTModelsSource(home))
	}
	chatGPTList = process.DiscoverChatGPTModels
)

// chatGPT is the ChatGPT subscription.
type chatGPT struct{}

func (chatGPT) ID() string { return chatGPTID }

func (chatGPT) Label() (en, zh string) { return "ChatGPT", "ChatGPT" }

// Flows is both ways in: the terminal sits in front of the user and can open
// the authorize URL on this machine, while a gateway elsewhere hands the URL
// over and takes the address the browser ended on.
func (chatGPT) Flows() []string { return []string{FlowBrowserCallback, FlowPasteCallback} }

// Probe answers whether the login this machine holds can list its models: a
// sign-in that still works is the finished sign-in, and no surface may push the
// user through a browser flow again for it.
func (chatGPT) Probe(ctx context.Context, env Env) turn.SignInCheck {
	return chatGPTProbe(ctx, env.Home)
}

// Start begins the sign-in. The loopback listener runs either way; the manual
// channel is what makes a gateway the browser cannot reach usable, and the
// authorization URL reaches the surface through the handle as soon as the flow
// has one.
func (chatGPT) Start(ctx context.Context, env Env, flow string) (Login, error) {
	switch flow {
	case FlowBrowserCallback, FlowPasteCallback:
	default:
		return nil, fmt.Errorf("ChatGPT has no %q sign-in", flow)
	}
	manual := make(chan openai.ManualCallback)
	return startLogin(ctx, manual, func(flowCtx context.Context, l *login) error {
		// out is discarded: the surface is the progress display, and a URL it
		// renders is the handle's, not a log line's.
		_, err := chatGPTLogin(flowCtx, openai.CredentialsPath(env.Home), io.Discard, func(url string) error {
			l.setURL(url)
			return nil
		}, manual)
		return err
	}), nil
}

// Offers lists the models this account may run, each on the Codex backend the
// login fixes.
func (chatGPT) Offers(ctx context.Context, env Env) ([]Offer, error) {
	records, err := chatGPTList(ctx, env.Home)
	if err != nil {
		return nil, err
	}
	return offersFromRecords(chatGPTID, records, openai.CodexBaseURL), nil
}

// Provision writes the chosen models as rows. There is no key to store: the
// login is the credential, and the row names it.
func (chatGPT) Provision(ctx context.Context, env Env, offers []Offer, opts ProvisionOptions) ([]string, error) {
	return provisionRows(ctx, env, offers, chatGPTID)
}

// RefreshKey has nothing to fetch: a ChatGPT row holds no key.
func (chatGPT) RefreshKey(ctx context.Context, env Env, modelID string) error {
	return ErrNoKeyToRefresh
}
