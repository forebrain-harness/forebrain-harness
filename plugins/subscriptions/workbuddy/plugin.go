// plugin.go is the plugin's front door: it registers the WorkBuddy provider
// (pkg/config), its LLM factory (pkg/llm), and its sign-in (pkg/subscription)
// the moment the binary blank-imports this package — which
// scripts/gen-subscription-plugins.sh arranges. Nothing here runs when the
// plugin directory is absent from a build.
package workbuddy

import (
	"context"
	"errors"
	"strconv"
	"strings"
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
// model row: a Chat Completions client whose transport is the disguise, wrapped
// so a spent free tier reaches the runtime as the choice it is.
func newWorkBuddyLLM(req llm.ProviderLLMRequest) (llm.LLM, error) {
	// The endpoint is the login's, not the row's: the transport owns it.
	root, err := home.Root()
	if err != nil {
		return nil, err
	}
	// The reader's answer to the switch is written into this plugin's own record,
	// which the transport reads before every send. It is registered here rather
	// than in init() because it needs the home the rest of the plugin runs
	// against, and a factory is the first place one exists.
	llm.RegisterPaidTier("workbuddy", workBuddyPaidTier{home: root})
	inner := openai.NewCompatLLMWithTransport(
		"",                     // the login is the credential
		workBuddyBaseURL+"/v2", // rewritten by the transport anyway
		req.Model,
		0,
		req.MaxOutputTokens,
		"",
		req.ParamsJSON,
		&Transport{Home: root},
	)
	return &paidTierLLM{inner: inner, model: strings.TrimSpace(req.Model)}, nil
}

// paidTierLLM names the one failure a reader can answer: the transport has
// already recognized a spent free tier and rewritten it into an OpenAI-shaped
// error, and this turns it into the offer a surface draws.
type paidTierLLM struct {
	inner llm.LLM
	// model is the row's model — the free tier, since a paid twin is never a row
	// of this plugin's picker.
	model string
}

func (p *paidTierLLM) Execute(ctx context.Context, messages []llm.Message, tools []*llm.Tool) (*llm.Result, error) {
	result, err := p.inner.Execute(ctx, messages, tools)
	return result, p.classify(err)
}

// classify answers the refusal as a PaidTierError when the reader has a paid twin
// to switch to, and hands every other failure on untouched. The three conditions
// are the client's own: the row is a declared free tier, the product allows the
// switch, and the catalog carries the paid twin.
func (p *paidTierLLM) classify(err error) error {
	if err == nil {
		return nil
	}
	pair, ok := pairForFree(p.model)
	if !ok || !pair.AllowPaidSwitch || !catalogHasModel(pair.PaidID) {
		return err
	}
	var api *llm.APIError
	if !errors.As(err, &api) || api == nil || strings.TrimSpace(api.Code) != strconv.Itoa(capCode) {
		return err
	}
	resetAt, _ := parseCapResetAt(api.Message)
	return &llm.PaidTierError{
		Provider:    "workbuddy",
		FreeModel:   pair.FreeID,
		PaidModel:   pair.PaidID,
		DisplayName: pair.DisplayName,
		ResetAt:     resetAt,
		Err:         err,
	}
}

// workBuddyPaidTier is what the switch writes: the persistent half lives in the
// plugin's rate-limit record, keyed by the account the login names, and a hit the
// product stated no reset for keeps its choice in this process only.
type workBuddyPaidTier struct {
	home string
}

func (p workBuddyPaidTier) UsePaidTier(ctx context.Context, freeModel string, resetAt time.Time) error {
	creds, ok, err := loadCredentials(ctx, nil, p.home)
	if err != nil {
		return err
	}
	if !ok {
		return signInRequired(nil)
	}
	return optInPaidTier(p.home, creds, freeModel, resetAt)
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
//
// A free/paid pair is one row for an account the product's paid line covers
// (the official client's filterVisiblePaidLineModels): the offer names the free
// tier and carries the paid twin as metadata, so a surface shows one row and the
// transport can switch it. An account outside that line gets both rows, named
// apart, because the client offers both there too.
func (workBuddySubscription) Offers(ctx context.Context, env subscription.Env) ([]subscription.Offer, error) {
	resolved := fetchCatalog(ctx, env.DB, env.Home)
	installBuiltinModels(resolved.records)
	eligible := resolved.signedIn && paidLineEligible(resolved.account)
	pairs := make(map[string]ratePair, len(resolved.pairs))
	listed := make(map[string]bool, len(resolved.records))
	for _, record := range resolved.records {
		listed[record.ID] = true
	}
	for _, pair := range resolved.pairs {
		pairs[pair.FreeID] = pair
	}
	offers := make([]subscription.Offer, 0, len(resolved.records))
	for _, record := range resolved.records {
		// The paid row of a pair the account can switch is not a row of its
		// own: requests reach it through the switch, and provisioning it would
		// give the picker the second identically-named row this replaces.
		if eligible && pairedPaidRow(record.ID, resolved.pairs) {
			continue
		}
		offer := subscription.Offer{
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
			FromStaticList:   resolved.fromStatic,
		}
		if pair, ok := pairs[record.ID]; ok && listed[pair.PaidID] {
			offer.PaidModel = pair.PaidID
			offer.AllowPaidSwitch = pair.AllowPaidSwitch
		}
		offers = append(offers, offer)
	}
	return offers, nil
}

// pairedPaidRow reports whether id is the paid twin of a pair whose free row the
// same catalog carries: without that free row the paid id is a model in its own
// right and stays on the picker.
func pairedPaidRow(id string, pairs []ratePair) bool {
	for _, pair := range pairs {
		if pair.PaidID == id && pair.FreeID != id {
			return true
		}
	}
	return false
}

// Provision writes the chosen models as login-backed rows.
func (workBuddySubscription) Provision(ctx context.Context, env subscription.Env, offers []subscription.Offer, _ subscription.ProvisionOptions) ([]string, error) {
	return subscription.ProvisionRows(ctx, env, offers, "workbuddy")
}

// RefreshKey: the login is the credential; there is no key to fetch again.
func (workBuddySubscription) RefreshKey(context.Context, subscription.Env, string) error {
	return subscription.ErrNoKeyToRefresh
}
