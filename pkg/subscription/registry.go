// The subscription registry: the one list of sign-ins, and the contract every
// surface drives them through.
package subscription

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/forebrain-harness/forebrain-harness/pkg/llm/openai"
	"github.com/forebrain-harness/forebrain-harness/pkg/process"
	"github.com/forebrain-harness/forebrain-harness/pkg/turn"
)

// Flow kinds a surface knows how to drive. A flow never opens a browser for
// the user: only a surface knows whether this machine is the one with their
// browser, so a surface that can opens AuthorizeURL itself and one that cannot
// shows it.
const (
	FlowBrowserCallback = "browser_callback" // open a URL; the redirect lands on a loopback server (TUI on the same machine)
	FlowPasteCallback   = "paste_callback"   // open a URL; paste back the address the browser ended on (ChatGPT on a remote gateway)
	FlowDeviceCode      = "device_code"      // show a user code and the verification URL, and poll while the user confirms it (OpenCode; Volcengine in plan 281)
)

// Subscription is one sign-in-backed model source. Every surface lists them
// from Registry() and drives them through this interface only.
type Subscription interface {
	ID() string
	Label() (en, zh string)
	// Flows lists the flow kinds this subscription supports, best first; a
	// surface picks the first one it can drive (the gateway cannot run a
	// loopback server for a remote browser, so it skips browser_callback).
	Flows() []string
	Probe(ctx context.Context, env Env) turn.SignInCheck
	Start(ctx context.Context, env Env, flow string) (Login, error)
	// Offers lists what a finished sign-in unlocks: one entry per model per
	// plan, each already carrying its endpoint.
	Offers(ctx context.Context, env Env) ([]Offer, error)
	// Provision writes the chosen offers as model rows (fetching any key the
	// plan needs) and returns their row ids, in input order.
	Provision(ctx context.Context, env Env, offers []Offer, opts ProvisionOptions) ([]string, error)
	// RefreshKey fetches the plan's key again for one row this subscription
	// wrote (the user rotated it in the provider's console) and stores it.
	// Login-backed subscriptions without keys return ErrNoKeyToRefresh.
	RefreshKey(ctx context.Context, env Env, modelID string) error
}

// Login is one running sign-in. A flow waits for a human in another window, so
// Start hands back this handle before the flow has finished; every answer it
// learns (the authorization URL, a device code) is readable while it runs, and
// the surface — never the flow — is what opens the browser.
type Login interface {
	AuthorizeURL() string
	UserCode() string                                // device_code only
	Submit(ctx context.Context, pasted string) error // paste_* only; error text is safe to show
	Wait(ctx context.Context) error                  // returns when signed in, failed, or ctx ends
	Cancel()
}

// Offer is one model a finished sign-in can write: the model, the endpoint its
// requests go to, and the plan inside the subscription it belongs to.
type Offer struct {
	Subscription   string // ID()
	Plan           string // "" | "coding-plan" | "agent-plan" | "coding-plan-team" | "agent-plan-team"
	PlanLabel      string // "Coding Plan" / "Agent Plan · 团队" …
	Provider       string
	Model          string
	BaseURL        string
	APIPath        string
	ContextWindow  int64
	SupportsImages bool
	Default        bool
}

// ProvisionOptions carries the choices a key-backed plan needs. ChatGPT and
// OpenCode Go ignore it; plan 281 (Volcengine) reads both fields.
type ProvisionOptions struct {
	// KeyID picks one key when a plan's key pool holds several; empty means
	// "the only Active one" and fails with ErrKeyChoice when there are several.
	KeyID string
	// CreateKey consents to creating one key when the pool is empty.
	CreateKey bool
}

// Env is what a subscription reads and writes: the Forebrain Harness home the
// credentials live under, and the state database a model row goes into.
type Env struct {
	Home string
	DB   *sql.DB
}

// KeyCandidate is one key a plan's pool holds, as the user picks between them.
// Tail is the key's masked end, never the key.
type KeyCandidate struct {
	ID, Name, Tail string
}

// ErrKeyChoice carries the candidate keys when a plan's pool holds several
// active ones.
type ErrKeyChoice struct {
	Keys []KeyCandidate
}

func (e ErrKeyChoice) Error() string {
	names := make([]string, 0, len(e.Keys))
	for _, key := range e.Keys {
		names = append(names, key.Name)
	}
	return "this plan holds several keys (" + strings.Join(names, ", ") + "); choose one"
}

// ErrNoKey says the plan's pool is empty and creating one needs the user's
// consent (ProvisionOptions.CreateKey).
var ErrNoKey = errors.New("no API key in this plan's pool; creating one needs your consent")

// ErrNoKeyToRefresh is the answer for a sign-in-backed row: there is no key to
// fetch again, because the login is the credential.
var ErrNoKeyToRefresh = errors.New("this subscription has no stored key to refresh")

// The two errors a Login handle answers when it cannot take a pasted address:
// the flow has no callback to hand over, or it already ended.
var (
	errNoPasteCallback = errors.New("this sign-in has no callback to paste")
	errLoginEnded      = errors.New("this sign-in has already ended")
)

// Registry is every subscription a surface offers, in display order. Only
// sign-ins are here: a provider a user connects with a pasted key belongs to
// the built-in provider registry (pkg/config).
func Registry() []Subscription {
	return []Subscription{chatGPT{}, openCodeGo{}}
}

// ByID finds one subscription by the id a surface persisted (a model row's
// source, or a login the user resumed).
func ByID(id string) (Subscription, bool) {
	want := strings.ToLower(strings.TrimSpace(id))
	for _, sub := range Registry() {
		if sub.ID() == want {
			return sub, true
		}
	}
	return nil, false
}

// login is the Login handle every flow returns. A flow runs in its own
// goroutine — it is waiting for a person — and reports what it learns here
// while the surface reads it.
type login struct {
	// manual is the channel a pasted callback goes into, nil for a flow that
	// has no callback to hand over (device code).
	manual chan openai.ManualCallback
	cancel context.CancelFunc
	// done is closed once the flow returned, so Wait, Submit and Cancel never
	// block on a flow that is over.
	done chan struct{}
	mu   sync.Mutex
	url  string
	code string
	err  error
}

// startLogin runs one flow in the background and returns its handle. run
// reports what the flow learns through the handle; its error is Wait's answer,
// and ctx is the caller's: it bounds the whole sign-in, so a canceled request
// aborts the flow instead of leaving a listener and a goroutine behind.
func startLogin(ctx context.Context, manual chan openai.ManualCallback,
	run func(ctx context.Context, l *login) error) *login {
	flowCtx, cancel := context.WithCancel(ctx)
	l := &login{manual: manual, cancel: cancel, done: make(chan struct{})}
	go func() {
		// The cancel runs last so a flow that returned on its own releases
		// what its context held, the same way the caller's own defer would.
		defer close(l.done)
		defer cancel()
		err := run(flowCtx, l)
		l.mu.Lock()
		l.err = err
		l.mu.Unlock()
	}()
	return l
}

// setURL records the authorization URL. The flow calls it from its own
// goroutine as soon as the provider hands one over; a surface polls it to show
// the page's link.
func (l *login) setURL(url string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.url = url
}

// setUserCode records the device flow's short code.
func (l *login) setUserCode(code string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.code = code
}

// AuthorizeURL is the URL the user has to open, empty until the flow knows it.
// The flow hands it over rather than opening anything itself: only a surface
// sitting in front of the user can open a browser there, and one running
// remotely must show the link instead.
func (l *login) AuthorizeURL() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.url
}

// UserCode is the code a device flow asks the user to confirm, empty until the
// flow knows it and for every flow that does not use one.
func (l *login) UserCode() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.code
}

// Submit hands over the address the browser ended on. The flow decides whether
// it belongs to this sign-in and answers through the reply: a rejected address
// leaves the login running, because the person who pasted it can try again.
func (l *login) Submit(ctx context.Context, pasted string) error {
	if l.manual == nil {
		return errNoPasteCallback
	}
	reply := make(chan error, 1)
	select {
	case l.manual <- openai.ManualCallback{URL: pasted, Reply: reply}:
	case <-l.done:
		return errLoginEnded
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-l.done:
		return errLoginEnded
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wait blocks until the flow ends: signed in, failed, or the caller's own
// context ended. A context that ends here cancels the flow — a caller that
// stopped waiting must not leave a listener and a goroutine running for a
// sign-in nobody will finish.
func (l *login) Wait(ctx context.Context) error {
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

// Cancel stops the flow. It is safe to call more than once, and after the flow
// ended it changes nothing.
func (l *login) Cancel() { l.cancel() }

// offersFromRecords is the one projection of a subscription's model listing
// into offers: the row a surface writes names the API model, the endpoint comes
// from the subscription (its login decides it, never the user), and the
// capability data comes from the listing.
func offersFromRecords(subscription string, records []turn.ModelRecord, baseURL string) []Offer {
	offers := make([]Offer, 0, len(records))
	for _, record := range records {
		offers = append(offers, Offer{
			Subscription:   subscription,
			Provider:       record.Provider,
			Model:          record.APIModel,
			BaseURL:        baseURL,
			ContextWindow:  record.ContextWindow,
			SupportsImages: record.SupportsAttachments,
			Default:        record.IsDefault,
		})
	}
	return offers
}

// provisionRows writes offers as model rows written by source and returns their
// ids in input order. A login-backed row holds no key: the login is the
// credential, and stating an empty key is not the same as saying nothing — a
// key left on the row belongs to whatever provider used it before and, for
// OpenCode, it outranks the login.
func provisionRows(ctx context.Context, env Env, offers []Offer, source string) ([]string, error) {
	ids := make([]string, 0, len(offers))
	for _, offer := range offers {
		empty := ""
		id, err := process.UpsertLLMModel(ctx, env.DB, process.LLMModelInput{
			Provider:   offer.Provider,
			Model:      offer.Model,
			BaseURL:    offer.BaseURL,
			APIPath:    offer.APIPath,
			APIKey:     &empty,
			Source:     source,
			SourcePlan: offer.Plan,
		})
		if err != nil {
			return nil, fmt.Errorf("store %s/%s: %w", offer.Provider, offer.Model, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
