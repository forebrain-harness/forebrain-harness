// plugin.go is the plugin's front door: it registers the qodercn provider
// (pkg/config), its LLM factory (pkg/llm), and its sign-in (pkg/subscription)
// the moment the binary blank-imports this package — which
// scripts/gen-subscription-plugins.sh arranges. Nothing here runs when the
// plugin directory is absent from a build.
package qodercn

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/subscription"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

func init() {
	// The static production table keeps capabilities (context windows,
	// effort levels) available before the first live listing replaces it.
	installBuiltinModels(staticQoderModels())
	config.RegisterProviderPlugin(config.BuiltinProvider{
		ID: "qodercn", Label: "Qoder CN", LabelZH: "Qoder CN",
		Hint: "Use your Qoder CN subscription", LoginBacked: true,
		Endpoints: []config.ProviderEndpoint{{
			BaseURL:     "https://gateway.qoder.com.cn",
			ChoiceLabel: "Sign in with Qoder CN",
			Hint:        "Browser authorization; use your Qoder CN plan quota",
		}},
	})
	llm.RegisterProviderFactory("qodercn", newQoderCNLLM)
	subscription.Register(qoderCNSubscription{})
}

// newQoderCNLLM is the factory the run configuration calls for a qodercn
// model row: a Chat Completions client whose transport is the disguise.
func newQoderCNLLM(req llm.ProviderLLMRequest) (llm.LLM, error) {
	// The endpoint is the login's, not the row's: the transport owns it.
	root, err := home.Root()
	if err != nil {
		return nil, err
	}
	transport := &Transport{Home: root}
	return openai.NewCompatLLMWithTransport(
		"",                             // the login is the credential
		"https://gateway.qoder.com.cn", // rewritten by the transport anyway
		req.Model,
		0,
		req.MaxOutputTokens,
		"",
		req.ParamsJSON,
		transport,
	), nil
}

// qoderCNSubscription is the sign-in surface entry: a browser PKCE flow that
// owns its own device token (the official client's session is never touched),
// offering the models the account's listing unlocks.
type qoderCNSubscription struct{}

func (qoderCNSubscription) ID() string             { return "qodercn" }
func (qoderCNSubscription) Label() (en, zh string) { return "Qoder CN", "Qoder CN" }
func (qoderCNSubscription) Flows() []string {
	return []string{subscription.FlowBrowserCallback, subscription.FlowDeviceCode}
}

// Probe answers how the sign-in stands: no login (or none adoptable) is
// missing, a login the gateway rejects is dead, an unreachable gateway keeps
// whatever was saved, and a token that refreshes is usable.
func (qoderCNSubscription) Probe(ctx context.Context, env subscription.Env) turn.SignInCheck {
	_, ok, err := tryAdoptLocalQoderCNCredentials(ctx, env.DB, env.Home)
	if err != nil {
		if isUnauthorized(err) {
			return turn.SignInCheck{State: turn.SignInDead, Err: err}
		}
		return turn.SignInCheck{State: turn.SignInUnreachable, Err: err}
	}
	if !ok {
		return turn.SignInCheck{State: turn.SignInMissing}
	}
	if _, err := EnsureFreshCredentials(ctx, env.DB, env.Home); err != nil {
		if isUnauthorized(err) {
			return turn.SignInCheck{State: turn.SignInDead, Err: err}
		}
		// The stored login exists; the network does not answer.
		return turn.SignInCheck{State: turn.SignInUnreachable, Err: err}
	}
	return turn.SignInCheck{State: turn.SignInUsable}
}

// isUnauthorized reports whether err is the 401 the surfaces read as "the
// login is gone".
func isUnauthorized(err error) bool {
	var api *llm.APIError
	return errors.As(err, &api) && api.StatusCode == http.StatusUnauthorized
}

// Start runs the browser PKCE device flow. The handle it returns publishes
// the authorization URL the moment the flow builds it; there is no short code
// to confirm — the browser page is the confirmation.
func (qoderCNSubscription) Start(ctx context.Context, env subscription.Env, flow string) (subscription.Login, error) {
	l := &qoderLogin{
		urlCh: make(chan string, 1),
		done:  make(chan struct{}),
	}
	l.cancel = context.CancelFunc(func() {})
	flowCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	go func() {
		defer close(l.done)
		creds, err := LoginPKCEDeviceFlow(flowCtx, env.DB, env.Home, func(u string) {
			l.mu.Lock()
			l.url = u
			l.mu.Unlock()
			select {
			case l.urlCh <- u:
			default:
			}
		})
		l.mu.Lock()
		l.err = err
		l.mu.Unlock()
		_ = creds
	}()
	return l, nil
}

// qoderLogin is the Login handle for the PKCE flow.
type qoderLogin struct {
	mu     sync.Mutex
	url    string
	err    error
	urlCh  chan string
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (l *qoderLogin) AuthorizeURL() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.url
}

// UserCode is always empty: this flow confirms in the browser, not with a
// typed code.
func (l *qoderLogin) UserCode() string { return "" }

// Submit answers that the flow takes no pasted callback: the browser page is
// the confirmation.
func (l *qoderLogin) Submit(context.Context, string) error {
	return errors.New("this sign-in confirms in the browser; there is no callback to paste")
}

func (l *qoderLogin) Wait(ctx context.Context) error {
	select {
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.err
	case <-ctx.Done():
		l.Cancel()
		return ctx.Err()
	}
}

func (l *qoderLogin) Cancel() { l.once.Do(func() { l.cancel() }) }

// Offers lists the models the account unlocks: the live listing when it
// answers, its fallbacks otherwise — always carrying the endpoint the
// transport owns.
func (qoderCNSubscription) Offers(ctx context.Context, env subscription.Env) ([]subscription.Offer, error) {
	records, err := FetchModels(ctx, env.DB, env.Home)
	if err != nil {
		return nil, err
	}
	installBuiltinModels(records)
	offers := make([]subscription.Offer, 0, len(records))
	for _, record := range records {
		levels, defaultEffort, off := record.thinkingLevels()
		offers = append(offers, subscription.Offer{
			Subscription:   "qodercn",
			Provider:       "qodercn",
			Model:          record.Key,
			Name:           record.DisplayName,
			BaseURL:        "https://gateway.qoder.com.cn",
			APIPath:        "/algo/api/v2/service/pro/sse/agent_chat_generation",
			ContextWindow:  record.contextWindow(),
			SupportsImages: record.IsVL,
			Default:        defaultCheckedModels[record.Key] || record.IsDefault,
			Efforts:        levels,
			DefaultEffort:  defaultEffort,
			// The listing is the closed answer to "can thinking be turned off":
			// a model without the switch states no.
			ReasoningOff: &off,
		})
	}
	return offers, nil
}

// Provision writes the chosen models as login-backed rows.
func (qoderCNSubscription) Provision(ctx context.Context, env subscription.Env, offers []subscription.Offer, _ subscription.ProvisionOptions) ([]string, error) {
	return subscription.ProvisionRows(ctx, env, offers, "qodercn")
}

// RefreshKey: the login is the credential; there is no key to fetch again.
func (qoderCNSubscription) RefreshKey(context.Context, subscription.Env, string) error {
	return subscription.ErrNoKeyToRefresh
}
