// plugin.go is the plugin's front door: it registers the WorkBuddy provider
// (pkg/config), its LLM factory (pkg/llm), and its sign-in (pkg/subscription)
// the moment the binary blank-imports this package — which
// scripts/gen-subscription-plugins.sh arranges. Nothing here runs when the
// plugin directory is absent from a build.
package workbuddy

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/forebrain-harness/forebrain-harness/pkg/config"
	"github.com/forebrain-harness/forebrain-harness/pkg/home"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm"
	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/subscription"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// The product identity this plugin dresses as, from the shipped
// cli/product.json: its productName, genieVersion, the platform its
// authentication block names, and the sign-in this plugin performs.
const (
	productName    = "WorkBuddy"
	productVersion = "5.9.3"
	// cliVersion is the agent CLI build the product ships beside the desktop
	// app (@genie/agent-cli, cli/package.json). The gateway answers its product
	// config per client identity, and only a User-Agent carrying
	// "CLI/<cliVersion>" gets the current catalog (Hy4, GLM-5.3, Space-Bunny,
	// …); without that segment the legacy desktop catalog (Hy3 and older)
	// comes back, which is what this plugin used to offer.
	cliVersion = "2.165.0"
	// deploymentType is product.json's deploymentType, the cloud product: the
	// official client states it as X-Product.
	deploymentType  = "SaaS"
	productPlatform = "workbuddy"
	// authMethod is the sign-in this plugin performs: X-Auth-Method carries it,
	// and X-Userinfo states it as token_source.
	authMethod = "browser"
)

// The injectable knobs every endpoint, poll interval and deadline reads, so a
// test (and only a test) can point the whole plugin at an httptest server.
var (
	// workBuddyBaseURL is the product endpoint (product.json `endpoint`): the
	// host every model request goes to. The credential's domain never overrides
	// it — it only feeds X-Domain.
	workBuddyBaseURL = "https://www.workbuddy.cn"
	// loginPollInterval and loginTimeout bound the browser sign-in: the official
	// client polls every second for at most five minutes.
	loginPollInterval = time.Second
	loginTimeout      = 300 * time.Second
)

func init() {
	// The shipped table keeps capabilities (context windows, thinking levels)
	// available before the first live listing replaces them.
	installBuiltinModels(chatModels(staticWorkBuddyModels()))
	config.RegisterProviderPlugin(config.BuiltinProvider{
		ID: "workbuddy", Label: "WorkBuddy", LabelZH: "WorkBuddy",
		Hint: "Use your WorkBuddy subscription", LoginBacked: true,
		Endpoints: []config.ProviderEndpoint{{
			BaseURL:     workBuddyBaseURL + "/v2",
			ChoiceLabel: "Sign in with WorkBuddy",
			Hint:        "Browser authorization; use your WorkBuddy plan quota",
		}},
	})
	llm.RegisterProviderFactory("workbuddy", newWorkBuddyLLM)
	subscription.Register(workBuddySubscription{})
}

// newWorkBuddyLLM is the factory the run configuration calls for a workbuddy
// model row: a Chat Completions client whose transport is the disguise.
func newWorkBuddyLLM(req llm.ProviderLLMRequest) (llm.LLM, error) {
	// The endpoint is the login's, not the row's: the transport owns it.
	root, err := home.Root()
	if err != nil {
		return nil, err
	}
	return openai.NewCompatLLMWithTransport(
		"",                     // the login is the credential
		workBuddyBaseURL+"/v2", // rewritten by the transport anyway
		req.Model,
		0,
		req.MaxOutputTokens,
		"",
		req.ParamsJSON,
		&Transport{Home: root},
	), nil
}

// workBuddySubscription is the sign-in surface entry: a browser sign-in that
// owns its own token (the desktop app's login is never read), offering the
// models the account's listings unlock.
type workBuddySubscription struct{}

func (workBuddySubscription) ID() string             { return "workbuddy" }
func (workBuddySubscription) Label() (en, zh string) { return "WorkBuddy", "WorkBuddy" }

// Flows declares both browser flows because a flow is a surface's vocabulary,
// not a protocol classification: the terminal drives the first one it can and
// opens the authorization URL itself, while the gateway (which cannot run a
// loopback server for a remote browser) skips browser_callback and shows the
// same URL for the user to open. This sign-in never has a code to confirm, so
// the web surface's wording comes from the frontend plugin's waitingHint and
// hideEmptyUserCode.
func (workBuddySubscription) Flows() []string {
	return []string{subscription.FlowBrowserCallback, subscription.FlowDeviceCode}
}

// Probe answers how the sign-in stands: no login is missing, a login the gateway
// rejects is dead, an unreachable gateway keeps whatever was saved, and a token
// that refreshes is usable.
func (workBuddySubscription) Probe(ctx context.Context, env subscription.Env) turn.SignInCheck {
	if _, ok, err := loadCredentials(ctx, env.DB, env.Home); err != nil {
		return turn.SignInCheck{State: turn.SignInUnreachable, Err: err}
	} else if !ok {
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

// Start runs the external-link sign-in. The handle it returns publishes the
// authorization URL the moment the login state is granted; there is no short
// code to confirm — the browser page is the confirmation.
func (workBuddySubscription) Start(ctx context.Context, env subscription.Env, flow string) (subscription.Login, error) {
	l := &workBuddyLogin{
		urlCh: make(chan string, 1),
		done:  make(chan struct{}),
	}
	flowCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	go func() {
		defer close(l.done)
		_, err := LoginExternalLink(flowCtx, env.DB, env.Home, func(u string) {
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
	}()
	return l, nil
}

// workBuddyLogin is the Login handle for the browser sign-in.
type workBuddyLogin struct {
	mu     sync.Mutex
	url    string
	err    error
	urlCh  chan string
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (l *workBuddyLogin) AuthorizeURL() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.url
}

// UserCode is always empty: this flow confirms in the browser, not with a typed
// code.
func (l *workBuddyLogin) UserCode() string { return "" }

// Submit answers that the flow takes no pasted callback: the browser page is the
// confirmation.
func (l *workBuddyLogin) Submit(context.Context, string) error {
	return errors.New("this sign-in confirms in the browser; there is no callback to paste")
}

func (l *workBuddyLogin) Wait(ctx context.Context) error {
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

func (l *workBuddyLogin) Cancel() { l.once.Do(func() { l.cancel() }) }

// Offers lists the models the account unlocks: the live listings when they
// answer, the shipped table otherwise — each offer carrying the endpoint the
// transport owns.
func (workBuddySubscription) Offers(ctx context.Context, env subscription.Env) ([]subscription.Offer, error) {
	records, fromStatic := FetchModels(ctx, env.DB, env.Home)
	installBuiltinModels(records)
	offers := make([]subscription.Offer, 0, len(records))
	for _, record := range records {
		offers = append(offers, subscription.Offer{
			Subscription:   "workbuddy",
			Provider:       "workbuddy",
			Model:          record.ID,
			Name:           record.Name,
			BaseURL:        workBuddyBaseURL + "/v2",
			ContextWindow:  record.MaxInputTokens,
			SupportsImages: record.SupportsImages,
			// What no source characterized stays unanswered, so the row does not
			// answer "no images" on a model nobody assessed.
			ImagesUndeclared: record.imagesUndeclared,
			Default:          record.IsDefault,
			Efforts:          effortLevels(record),
			DefaultEffort:    record.DefaultEffort,
			ReasoningOff:     record.CanDisableThinking,
			FromStaticList:   fromStatic,
		})
	}
	return offers, nil
}

// Provision writes the chosen models as login-backed rows.
func (workBuddySubscription) Provision(ctx context.Context, env subscription.Env, offers []subscription.Offer, _ subscription.ProvisionOptions) ([]string, error) {
	return subscription.ProvisionRows(ctx, env, offers, "workbuddy")
}

// RefreshKey: the login is the credential; there is no key to fetch again.
func (workBuddySubscription) RefreshKey(context.Context, subscription.Env, string) error {
	return subscription.ErrNoKeyToRefresh
}
